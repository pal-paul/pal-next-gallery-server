package moments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	Embedding           []float64 `json:"embedding,omitempty"`
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
	ListImageDescriptions(context.Context, []string) ([]ImageDescription, error)
	SaveImageDescriptions(context.Context, []ImageDescription) error
	CreateGeneratedMoment(context.Context, Moment, []Candidate) error
}

type Service struct {
	repository  Repository
	clusterer   Clusterer
	enricher    Enricher
	describer   ImageDescriber
	synthesizer MetadataSynthesizer
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
	prepared, err := service.prepareSemanticGroups(ctx, groups)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, preparedGroup := range prepared {
		group := preparedGroup.candidates
		if len(group) < MinimumMomentImages || (service.enricher == nil && (service.describer == nil || service.synthesizer == nil)) {
			continue
		}
		moment := generatedMoment(ownerID, group)
		var metadata Metadata
		if preparedGroup.descriptions != nil {
			metadata, err = service.synthesizer.Synthesize(ctx, moment, preparedGroup.descriptions)
		} else {
			metadata, err = service.enrichMoment(ctx, moment, semanticCandidates(group))
		}
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

type semanticGroup struct {
	candidates   []Candidate
	descriptions []ImageDescription
}

func (service *Service) prepareSemanticGroups(ctx context.Context, groups [][]Candidate) ([]semanticGroup, error) {
	merger, ok := service.synthesizer.(SemanticClusterMerger)
	if !ok || service.describer == nil {
		prepared := make([]semanticGroup, len(groups))
		for index, group := range groups {
			prepared[index].candidates = group
		}
		return prepared, nil
	}
	descriptionsByGroup := make([][]ImageDescription, len(groups))
	candidatesByGroup := make([][]Candidate, len(groups))
	mediaIDs := make([]string, 0)
	for groupIndex, group := range groups {
		candidatesByGroup[groupIndex] = clusterMergeCandidates(group)
		for _, candidate := range candidatesByGroup[groupIndex] {
			mediaIDs = append(mediaIDs, candidate.ID)
		}
	}
	storedDescriptions, err := service.repository.ListImageDescriptions(ctx, mediaIDs)
	if err != nil {
		return nil, err
	}
	descriptionsByID := make(map[string]ImageDescription, len(storedDescriptions))
	for _, description := range storedDescriptions {
		descriptionsByID[description.MediaID] = description
	}
	newDescriptions := make([]ImageDescription, 0)
	for groupIndex, candidates := range candidatesByGroup {
		for _, candidate := range candidates {
			description, exists := descriptionsByID[candidate.ID]
			if exists {
				descriptionsByGroup[groupIndex] = append(descriptionsByGroup[groupIndex], description)
				continue
			}
			description, err := service.describer.Describe(ctx, candidate)
			if err != nil {
				return nil, err
			}
			description.MediaID = candidate.ID
			descriptionsByGroup[groupIndex] = append(descriptionsByGroup[groupIndex], description)
			newDescriptions = append(newDescriptions, description)
		}
	}
	if len(newDescriptions) > 0 {
		if err := service.repository.SaveImageDescriptions(ctx, newDescriptions); err != nil {
			return nil, err
		}
	}
	mergedIndexes, err := merger.MergeClusters(ctx, descriptionsByGroup)
	if err != nil {
		return nil, err
	}
	if err := validateClusterPartition(mergedIndexes, len(groups)); err != nil {
		slog.WarnContext(ctx, "ignoring invalid semantic cluster partition", "error", err)
		mergedIndexes = identityClusterPartition(len(groups))
	}
	mergedIndexes = mergeCompatibleEventContextGroups(mergedIndexes, groups, descriptionsByGroup)
	prepared := make([]semanticGroup, 0, len(mergedIndexes))
	for _, indexes := range mergedIndexes {
		var merged semanticGroup
		for _, index := range indexes {
			merged.candidates = append(merged.candidates, groups[index]...)
			merged.descriptions = append(merged.descriptions, descriptionsByGroup[index]...)
		}
		prepared = append(prepared, merged)
	}
	return prepared, nil
}

func identityClusterPartition(clusterCount int) [][]int {
	partition := make([][]int, clusterCount)
	for index := range partition {
		partition[index] = []int{index}
	}
	return partition
}

type eventContext string

const (
	contextPublicVenue      eventContext = "public_venue"
	contextOutdoorActivity  eventContext = "outdoor_activity"
	contextHomeOrPrivate    eventContext = "home_or_private"
	contextEventCelebration eventContext = "event_or_celebration"
	contextTravel           eventContext = "travel"
	contextUnknown          eventContext = "unknown"
)

func mergeCompatibleEventContextGroups(partition [][]int, groups [][]Candidate, descriptions [][]ImageDescription) [][]int {
	merged := make([][]int, len(partition))
	for index := range partition {
		merged[index] = append([]int(nil), partition[index]...)
	}
	for left := 0; left < len(merged); left++ {
		leftContext := partitionEventContext(merged[left], descriptions)
		if leftContext == contextUnknown {
			continue
		}
		for right := left + 1; right < len(merged); {
			if partitionEventContext(merged[right], descriptions) == leftContext &&
				partitionsCompatible(merged[left], merged[right], groups) {
				merged[left] = append(merged[left], merged[right]...)
				merged = append(merged[:right], merged[right+1:]...)
				continue
			}
			right++
		}
	}
	for unknown := 0; unknown < len(merged); {
		if partitionEventContext(merged[unknown], descriptions) != contextUnknown {
			unknown++
			continue
		}
		match := -1
		bestAffinity := 0
		for known := range merged {
			if known == unknown || partitionEventContext(merged[known], descriptions) == contextUnknown ||
				!partitionsCompatible(merged[unknown], merged[known], groups) ||
				partitionEnvironment(merged[unknown], descriptions) != partitionEnvironment(merged[known], descriptions) {
				continue
			}
			affinity := partitionSemanticAffinity(merged[unknown], merged[known], descriptions)
			if affinity > bestAffinity {
				match, bestAffinity = known, affinity
			}
		}
		if match < 0 || bestAffinity < 2 {
			unknown++
			continue
		}
		merged[match] = append(merged[match], merged[unknown]...)
		merged = append(merged[:unknown], merged[unknown+1:]...)
	}
	return merged
}

func partitionEventContext(indexes []int, descriptions [][]ImageDescription) eventContext {
	context := contextUnknown
	for _, index := range indexes {
		if index < 0 || index >= len(descriptions) {
			return contextUnknown
		}
		classified := classifyEventContext(descriptions[index])
		if classified == contextUnknown {
			continue
		}
		if context != contextUnknown && context != classified {
			return contextUnknown
		}
		context = classified
	}
	return context
}

func classifyEventContext(descriptions []ImageDescription) eventContext {
	var text strings.Builder
	for _, description := range descriptions {
		fmt.Fprintf(&text, " %s %s %s %s %s", description.LocationType, description.Scene,
			description.Description, strings.Join(description.Activities, " "), strings.Join(description.Objects, " "))
	}
	corpus := strings.ToLower(text.String())
	privateTerms := []string{"living room", "bedroom", "my kitchen", "at home", "in our home", "home interior",
		"on the sofa", "on the couch", "on the bed", "private garden", "backyard", "family living room", "bedspread"}
	if containsAny(corpus, privateTerms...) {
		return contextHomeOrPrivate
	}
	eventTerms := []string{"wedding", "concert", "festival", "fairground", "conference", "school event",
		"graduation", "birthday party", "celebration", "ceremony", "auditorium", "event venue"}
	if containsAny(corpus, eventTerms...) {
		return contextEventCelebration
	}
	travelTerms := []string{"airport terminal", "train station", "ferry terminal", "hotel lobby", "hotel reception",
		"vacation", "sightseeing", "tourist", "resort", "cruise", "observation deck", "viewpoint"}
	if containsAny(corpus, travelTerms...) {
		return contextTravel
	}
	publicVenueTerms := []string{
		"museum", "art gallery", "exhibition hall", "exhibition", "art installation", "cultural center", "cultural centre",
		"theatre", "theater", "opera house", "shopping mall", "shopping centre", "shopping center", "department store",
		"supermarket", "market hall", "boutique", "outlet store", "food court", "restaurant", "cafe", "coffee shop",
		"bakery", "ice cream shop", "zoo", "aquarium", "theme park", "amusement park", "botanical garden",
		"planetarium", "castle", "palace", "historic site", "landmark", "monument", "city square", "public plaza",
		"fountain square", "concert hall", "convention center", "convention centre", "sports arena", "stadium",
		"bowling alley", "golf course", "public swimming pool", "public library", "university campus", "science center",
		"science centre", "public market", "atrium", "storefront", "cultural display", "decorative display", "statue", "sculpture",
	}
	if containsAny(corpus, publicVenueTerms...) {
		return contextPublicVenue
	}
	outdoorTerms := []string{"outdoor", " park", "beach", "hiking", "picnic", "garden", "trail", "forest", "mountain", "lake"}
	if containsAny(corpus, outdoorTerms...) {
		return contextOutdoorActivity
	}
	return contextUnknown
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

func partitionEnvironment(indexes []int, descriptions [][]ImageDescription) string {
	var corpus strings.Builder
	for _, index := range indexes {
		for _, description := range descriptions[index] {
			fmt.Fprintf(&corpus, " %s %s %s", description.LocationType, description.Scene, description.Description)
		}
	}
	value := strings.ToLower(corpus.String())
	if containsAny(value, "outdoor", "beach", " park", "garden", "forest", "mountain", "lake") {
		return "outdoor"
	}
	if containsAny(value, "indoor", "interior", "inside") {
		return "indoor"
	}
	return "unknown"
}

func partitionSemanticAffinity(left, right []int, descriptions [][]ImageDescription) int {
	leftTerms := partitionSemanticTerms(left, descriptions)
	rightTerms := partitionSemanticTerms(right, descriptions)
	shared := 0
	for term := range leftTerms {
		if _, exists := rightTerms[term]; exists {
			shared++
		}
	}
	return shared
}

func partitionSemanticTerms(indexes []int, descriptions [][]ImageDescription) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, index := range indexes {
		for _, description := range descriptions[index] {
			value := strings.ToLower(description.Scene + " " + description.Description + " " + strings.Join(description.Objects, " "))
			for _, term := range strings.FieldsFunc(value, func(character rune) bool { return character < 'a' || character > 'z' }) {
				if len(term) < 4 || containsAny(term, "indoor", "photo", "person", "people", "shows", "visible") {
					continue
				}
				if strings.HasPrefix(term, "decorat") {
					term = "decorat"
				}
				terms[term] = struct{}{}
			}
		}
	}
	return terms
}

func partitionsCompatible(left, right []int, groups [][]Candidate) bool {
	return partitionsWithinGap(left, right, groups, SessionGap) && partitionsLocationCompatible(left, right, groups)
}

func partitionsLocationCompatible(left, right []int, groups [][]Candidate) bool {
	foundLocations := false
	for _, leftIndex := range left {
		for _, rightIndex := range right {
			for _, leftCandidate := range groups[leftIndex] {
				for _, rightCandidate := range groups[rightIndex] {
					if leftCandidate.Latitude == nil || leftCandidate.Longitude == nil ||
						rightCandidate.Latitude == nil || rightCandidate.Longitude == nil {
						continue
					}
					foundLocations = true
					if coordinateDistanceKM(*leftCandidate.Latitude, *leftCandidate.Longitude,
						*rightCandidate.Latitude, *rightCandidate.Longitude) <= 50 {
						return true
					}
				}
			}
		}
	}
	return !foundLocations
}

func coordinateDistanceKM(latitude1, longitude1, latitude2, longitude2 float64) float64 {
	const earthRadiusKM = 6371.0
	latitudeDelta := (latitude2 - latitude1) * math.Pi / 180
	longitudeDelta := (longitude2 - longitude1) * math.Pi / 180
	leftLatitude := latitude1 * math.Pi / 180
	rightLatitude := latitude2 * math.Pi / 180
	a := math.Sin(latitudeDelta/2)*math.Sin(latitudeDelta/2) +
		math.Cos(leftLatitude)*math.Cos(rightLatitude)*math.Sin(longitudeDelta/2)*math.Sin(longitudeDelta/2)
	return earthRadiusKM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func partitionsWithinGap(left, right []int, groups [][]Candidate, gap time.Duration) bool {
	for _, leftIndex := range left {
		for _, rightIndex := range right {
			for _, leftCandidate := range groups[leftIndex] {
				for _, rightCandidate := range groups[rightIndex] {
					if leftCandidate.CapturedAt.Sub(rightCandidate.CapturedAt).Abs() <= gap {
						return true
					}
				}
			}
		}
	}
	return false
}

func validateClusterPartition(partition [][]int, clusterCount int) error {
	seen := make([]bool, clusterCount)
	for _, group := range partition {
		if len(group) == 0 {
			return fmt.Errorf("semantic cluster merger returned an empty group")
		}
		for _, index := range group {
			if index < 0 || index >= clusterCount || seen[index] {
				return fmt.Errorf("semantic cluster merger returned invalid index %d", index)
			}
			seen[index] = true
		}
	}
	for index, included := range seen {
		if !included {
			return fmt.Errorf("semantic cluster merger omitted index %d", index)
		}
	}
	return nil
}

func (service *Service) enrichMoment(ctx context.Context, moment Moment, candidates []Candidate) (Metadata, error) {
	if service.describer == nil || service.synthesizer == nil {
		return service.enricher.Enrich(ctx, moment, candidates)
	}
	descriptions := make([]ImageDescription, 0, len(candidates))
	for _, candidate := range candidates {
		description, err := service.describer.Describe(ctx, candidate)
		if err != nil {
			return Metadata{}, err
		}
		description.MediaID = candidate.ID
		descriptions = append(descriptions, description)
	}
	if err := service.repository.SaveImageDescriptions(ctx, descriptions); err != nil {
		return Metadata{}, err
	}
	return service.synthesizer.Synthesize(ctx, moment, descriptions)
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

func clusterMergeCandidates(candidates []Candidate) []Candidate {
	representatives := make([]Candidate, 0, min(len(candidates), MaximumSemanticImages))
	for _, candidate := range candidates {
		if candidate.Representative {
			representatives = append(representatives, candidate)
			if len(representatives) == MaximumSemanticImages {
				break
			}
		}
	}
	if len(representatives) > 0 {
		return representatives
	}
	return semanticCandidates(candidates)
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
