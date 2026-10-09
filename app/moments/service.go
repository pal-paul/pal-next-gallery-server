package moments

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	SessionGap              = 30 * time.Minute
	CandidateWindow         = 7 * 24 * time.Hour
	MinimumMomentImages     = 3
	MinimumDescriptionMatch = 0.65
	MaximumSemanticImages   = 15
)

var (
	ErrNotFound           = errors.New("moment not found")
	ErrCandidatesAssigned = errors.New("moment candidates already assigned")
)

type Moment struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"ownerId"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	StartTime    time.Time `json:"startTime"`
	EndTime      time.Time `json:"endTime"`
	LocationName string    `json:"locationName,omitempty"`
	ImageCount   int64     `json:"imageCount"`
	CoverMediaID string    `json:"coverMediaId,omitempty"`
	UserEdited   bool      `json:"userEdited"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Media        []Media   `json:"media,omitempty"`
}

type Media struct {
	ID             string    `json:"id"`
	Filename       string    `json:"fileName"`
	ThumbnailURL   string    `json:"thumbnailUrl,omitempty"`
	CapturedAt     time.Time `json:"capturedAt"`
	Representative bool      `json:"representative"`
}

type Candidate struct {
	ID                  string    `json:"id"`
	SourcePath          string    `json:"sourcePath"`
	CapturedAt          time.Time `json:"capturedAt"`
	Latitude            *float64  `json:"latitude,omitempty"`
	Longitude           *float64  `json:"longitude,omitempty"`
	SimilarityScore     float64   `json:"similarityScore,omitempty"`
	RepresentativeScore float64   `json:"representativeScore,omitempty"`
	Representative      bool      `json:"representative,omitempty"`
}

type Repository interface {
	ListMomentOwners(context.Context) ([]string, error)
	ListMoments(context.Context, string) ([]Moment, error)
	GetMoment(context.Context, string, string) (Moment, error)
	UpdateMoment(context.Context, string, string, string, string, string) error
	DeleteMoment(context.Context, string, string) error
	AddMomentMedia(context.Context, string, string, []string) error
	RemoveMomentMedia(context.Context, string, string, string) error
	SetMomentCover(context.Context, string, string, string) error
	ListMomentCandidates(context.Context, string) ([]Candidate, error)
	CreateGeneratedMoment(context.Context, Moment, []Candidate) error
}

type Service struct {
	repository Repository
	clusterer  Clusterer
	enricher   Enricher
}

func New(repository Repository, options ...Option) *Service {
	service := &Service{repository: repository, clusterer: temporalClusterer{gap: SessionGap}}
	for _, option := range options {
		option(service)
	}
	return service
}

func RegisterRoutes(router gin.IRoutes, service *Service) {
	router.GET("/moments", service.List)
	router.POST("/moments/generate", service.Generate)
	router.GET("/moments/:id", service.Get)
	router.PATCH("/moments/:id", service.Update)
	router.DELETE("/moments/:id", service.Delete)
	router.POST("/moments/:id/media", service.AddMedia)
	router.DELETE("/moments/:id/media/:mediaID", service.RemoveMedia)
	router.PATCH("/moments/:id/cover", service.SetCover)
}

func (service *Service) List(ctx *gin.Context) {
	items, err := service.repository.ListMoments(ctx, currentUser(ctx).ID)
	respond(ctx, items, err)
}

func (service *Service) Get(ctx *gin.Context) {
	item, err := service.repository.GetMoment(ctx, ctx.Param("id"), currentUser(ctx).ID)
	respond(ctx, item, err)
}

func (service *Service) Update(ctx *gin.Context) {
	var request struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	request.Status = "draft"
	if ctx.ShouldBindJSON(&request) != nil || strings.TrimSpace(request.Title) == "" || !validStatus(request.Status) {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "title and a valid status are required"})
		return
	}
	err := service.repository.UpdateMoment(ctx, ctx.Param("id"), currentUser(ctx).ID,
		strings.TrimSpace(request.Title), strings.TrimSpace(request.Description), request.Status)
	respondNoContent(ctx, err)
}

func (service *Service) Delete(ctx *gin.Context) {
	respondNoContent(ctx, service.repository.DeleteMoment(ctx, ctx.Param("id"), currentUser(ctx).ID))
}

func (service *Service) AddMedia(ctx *gin.Context) {
	var request struct {
		MediaIDs []string `json:"mediaIds"`
	}
	if ctx.ShouldBindJSON(&request) != nil || len(request.MediaIDs) == 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "mediaIds are required"})
		return
	}
	respondNoContent(ctx, service.repository.AddMomentMedia(ctx, ctx.Param("id"), currentUser(ctx).ID, request.MediaIDs))
}

func (service *Service) RemoveMedia(ctx *gin.Context) {
	respondNoContent(ctx, service.repository.RemoveMomentMedia(ctx, ctx.Param("id"), currentUser(ctx).ID, ctx.Param("mediaID")))
}

func (service *Service) SetCover(ctx *gin.Context) {
	var request struct {
		MediaID string `json:"mediaId"`
	}
	if ctx.ShouldBindJSON(&request) != nil || strings.TrimSpace(request.MediaID) == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "mediaId is required"})
		return
	}
	respondNoContent(ctx, service.repository.SetMomentCover(ctx, ctx.Param("id"), currentUser(ctx).ID, request.MediaID))
}

func (service *Service) Generate(ctx *gin.Context) {
	ownerID := currentUser(ctx).ID
	created, err := service.generateForOwner(ctx, ownerID)
	if err != nil {
		respond(ctx, nil, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"created": created})
}

func (service *Service) Run(ctx context.Context) error {
	ownerIDs, err := service.repository.ListMomentOwners(ctx)
	if err != nil {
		return err
	}
	for _, ownerID := range ownerIDs {
		if _, err := service.generateForOwner(ctx, ownerID); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) RunScheduled(ctx context.Context, interval time.Duration, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.Run(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

func (service *Service) generateForOwner(ctx context.Context, ownerID string) (int, error) {
	candidates, err := service.repository.ListMomentCandidates(ctx, ownerID)
	if err != nil {
		return 0, err
	}
	candidates = recentCandidates(candidates, time.Now().UTC(), CandidateWindow)
	groups, err := service.clusterer.Cluster(ctx, candidates)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, group := range groups {
		if len(group) < MinimumMomentImages || service.enricher == nil {
			continue
		}
		moment := generatedMoment(ownerID, group)
		metadata, err := service.enricher.Enrich(ctx, moment, semanticCandidates(group))
		if err != nil {
			slog.WarnContext(ctx, "moment metadata enrichment failed", "owner_id", ownerID, "error", err)
			continue
		}
		if metadata.Confidence < MinimumDescriptionMatch || strings.TrimSpace(metadata.Description) == "" {
			slog.InfoContext(ctx, "moment skipped due to low description match", "owner_id", ownerID,
				"match", metadata.Confidence, "minimum", MinimumDescriptionMatch)
			continue
		}
		moment.Title = strings.TrimSpace(metadata.Title)
		moment.Description = strings.TrimSpace(metadata.Description)
		if err := service.repository.CreateGeneratedMoment(ctx, moment, group); errors.Is(err, ErrCandidatesAssigned) {
			continue
		} else if err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func recentCandidates(candidates []Candidate, now time.Time, window time.Duration) []Candidate {
	cutoff := now.Add(-window)
	recent := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.CapturedAt.Before(cutoff) && !candidate.CapturedAt.After(now) {
			recent = append(recent, candidate)
		}
	}
	return recent
}

func clusterByTime(candidates []Candidate, gap time.Duration) [][]Candidate {
	ordered := append([]Candidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CapturedAt.Equal(ordered[j].CapturedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].CapturedAt.Before(ordered[j].CapturedAt)
	})
	groups := make([][]Candidate, 0)
	for _, candidate := range ordered {
		if len(groups) == 0 || candidate.CapturedAt.Sub(groups[len(groups)-1][len(groups[len(groups)-1])-1].CapturedAt) > gap {
			groups = append(groups, []Candidate{candidate})
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], candidate)
	}
	return groups
}

func generatedMoment(ownerID string, candidates []Candidate) Moment {
	start := candidates[0].CapturedAt.UTC()
	end := candidates[len(candidates)-1].CapturedAt.UTC()
	title := start.Format("January 2, 2006")
	if start.YearDay() != end.YearDay() || start.Year() != end.Year() {
		title = start.Format("January 2") + " - " + end.Format("January 2, 2006")
	}
	coverMediaID := ""
	bestScore := -1.0
	for _, candidate := range candidates {
		if candidate.Representative && candidate.RepresentativeScore > bestScore {
			coverMediaID = candidate.ID
			bestScore = candidate.RepresentativeScore
		}
	}
	if coverMediaID == "" {
		coverMediaID = candidates[0].ID
		bestScore = candidates[0].RepresentativeScore
		for _, candidate := range candidates[1:] {
			if candidate.RepresentativeScore > bestScore {
				coverMediaID = candidate.ID
				bestScore = candidate.RepresentativeScore
			}
		}
	}
	return Moment{
		ID: uuid.NewString(), OwnerID: ownerID, Title: title, Status: "draft",
		StartTime: start, EndTime: end, ImageCount: int64(len(candidates)), CoverMediaID: coverMediaID,
	}
}

func semanticCandidates(candidates []Candidate) []Candidate {
	selected := make([]Candidate, 0, min(len(candidates), MaximumSemanticImages))
	selectedIDs := make(map[string]struct{}, MaximumSemanticImages)
	add := func(candidate Candidate) {
		if len(selected) >= MaximumSemanticImages {
			return
		}
		if _, exists := selectedIDs[candidate.ID]; exists {
			return
		}
		selected = append(selected, candidate)
		selectedIDs[candidate.ID] = struct{}{}
	}
	for _, candidate := range candidates {
		if candidate.Representative {
			add(candidate)
		}
	}
	if len(candidates) > 1 {
		for index := 0; index < MaximumSemanticImages; index++ {
			add(candidates[index*(len(candidates)-1)/(MaximumSemanticImages-1)])
		}
	}
	for _, candidate := range candidates {
		add(candidate)
	}
	return selected
}

func validStatus(status string) bool {
	return status == "draft" || status == "published" || status == "archived"
}

func currentUser(ctx *gin.Context) auth.User {
	value, _ := ctx.Get(auth.UserContextKey)
	user, _ := value.(auth.User)
	return user
}

func respond(ctx *gin.Context, value any, err error) {
	if errors.Is(err, ErrNotFound) {
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		_ = ctx.Error(err)
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	ctx.JSON(http.StatusOK, value)
}

func respondNoContent(ctx *gin.Context, err error) {
	if err != nil {
		respond(ctx, nil, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
