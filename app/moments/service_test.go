package moments

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
)

type testRepository struct {
	owners     []string
	candidates []Candidate
	created    []Moment
	groups     [][]Candidate
	updated    Moment
}

type testClusterer struct{ groups [][]Candidate }

func (clusterer testClusterer) Cluster(context.Context, []Candidate) ([][]Candidate, error) {
	return clusterer.groups, nil
}

type testEnricher struct{ confidence float64 }

func (enricher testEnricher) Enrich(context.Context, Moment, []Candidate) (Metadata, error) {
	return Metadata{Title: "A day by the water", Description: "Sunny outdoor photos.", Confidence: enricher.confidence}, nil
}

func (repository *testRepository) ListMomentOwners(context.Context) ([]string, error) {
	return repository.owners, nil
}
func (repository *testRepository) ListMoments(context.Context, string) ([]Moment, error) {
	return repository.created, nil
}
func (repository *testRepository) GetMoment(_ context.Context, id, ownerID string) (Moment, error) {
	return Moment{ID: id, OwnerID: ownerID}, nil
}
func (repository *testRepository) UpdateMoment(_ context.Context, id, ownerID, title, description, status string) error {
	repository.updated = Moment{ID: id, OwnerID: ownerID, Title: title, Description: description, Status: status}
	return nil
}
func (repository *testRepository) DeleteMoment(context.Context, string, string) error { return nil }
func (repository *testRepository) AddMomentMedia(context.Context, string, string, []string) error {
	return nil
}
func (repository *testRepository) RemoveMomentMedia(context.Context, string, string, string) error {
	return nil
}
func (repository *testRepository) SetMomentCover(context.Context, string, string, string) error {
	return nil
}
func (repository *testRepository) ListMomentCandidates(context.Context, string) ([]Candidate, error) {
	return repository.candidates, nil
}
func (repository *testRepository) CreateGeneratedMoment(_ context.Context, moment Moment, candidates []Candidate) error {
	repository.created = append(repository.created, moment)
	repository.groups = append(repository.groups, candidates)
	return nil
}

func TestGenerateCreatesTimeBasedMomentsAndLeavesSingletons(t *testing.T) {
	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	repository := &testRepository{candidates: []Candidate{
		{ID: "late-2", CapturedAt: base.Add(2*time.Hour + 10*time.Minute)},
		{ID: "first", CapturedAt: base},
		{ID: "third", CapturedAt: base.Add(20 * time.Minute)},
		{ID: "single", CapturedAt: base.Add(3 * time.Hour)},
		{ID: "late-1", CapturedAt: base.Add(2 * time.Hour)},
		{ID: "late-3", CapturedAt: base.Add(2*time.Hour + 20*time.Minute)},
		{ID: "second", CapturedAt: base.Add(30 * time.Minute)},
	}}
	router := testRouterWithService(New(repository, WithEnricher(testEnricher{confidence: 0.65})))
	request := httptest.NewRequest(http.MethodPost, "/moments/generate", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != `{"created":2}` {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
	if len(repository.groups) != 2 || len(repository.groups[0]) != 3 || len(repository.groups[1]) != 3 {
		t.Fatalf("unexpected generated groups: %#v", repository.groups)
	}
	if repository.created[0].OwnerID != "user-1" || repository.created[0].Status != "draft" {
		t.Fatalf("unexpected generated moment: %#v", repository.created[0])
	}
	if repository.created[0].CoverMediaID != "first" || repository.created[0].Title != "A day by the water" {
		t.Fatalf("unexpected generated metadata: %#v", repository.created[0])
	}
}

func TestRecentCandidatesKeepsOnlyPreviousSevenDays(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "cutoff", CapturedAt: now.Add(-CandidateWindow)},
		{ID: "recent", CapturedAt: now.Add(-time.Hour)},
		{ID: "now", CapturedAt: now},
		{ID: "old", CapturedAt: now.Add(-CandidateWindow - time.Nanosecond)},
		{ID: "future", CapturedAt: now.Add(time.Nanosecond)},
	}

	recent := recentCandidates(candidates, now, CandidateWindow)
	if len(recent) != 3 || recent[0].ID != "cutoff" || recent[1].ID != "recent" || recent[2].ID != "now" {
		t.Fatalf("unexpected recent candidates: %#v", recent)
	}
}

func TestUpdateMarksAuthenticatedUsersMoment(t *testing.T) {
	repository := &testRepository{}
	router := testRouter(repository)
	request := httptest.NewRequest(http.MethodPatch, "/moments/moment-1",
		bytes.NewBufferString(`{"title":"  Summer day  ","description":"  By the water  ","status":"published"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
	if repository.updated.OwnerID != "user-1" || repository.updated.Title != "Summer day" ||
		repository.updated.Description != "By the water" || repository.updated.Status != "published" {
		t.Fatalf("unexpected update: %#v", repository.updated)
	}
}

func TestGenerateUsesVisualRepresentativeAndEnrichment(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	group := []Candidate{
		{ID: "first", CapturedAt: base, RepresentativeScore: 0.2},
		{ID: "sharp", CapturedAt: base.Add(time.Minute), Representative: true, RepresentativeScore: 0.9},
		{ID: "third", CapturedAt: base.Add(2 * time.Minute), RepresentativeScore: 0.3},
	}
	repository := &testRepository{candidates: group}
	service := New(repository, WithClusterer(testClusterer{groups: [][]Candidate{group}}), WithEnricher(testEnricher{confidence: 0.9}))
	router := testRouterWithService(service)
	request := httptest.NewRequest(http.MethodPost, "/moments/generate", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
	created := repository.created[0]
	if created.CoverMediaID != "sharp" || created.Title != "A day by the water" || created.Description != "Sunny outdoor photos." {
		t.Fatalf("unexpected enriched moment: %#v", created)
	}
}

func TestGeneratedMomentUsesHighestScoredRepresentativeAsCover(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	candidates := []Candidate{
		{ID: "first", CapturedAt: base, Representative: true, RepresentativeScore: 0.9},
		{ID: "middle", CapturedAt: base.Add(time.Minute), RepresentativeScore: 1},
		{ID: "last", CapturedAt: base.Add(2 * time.Minute), Representative: true, RepresentativeScore: 0.4},
	}

	moment := generatedMoment("user-1", candidates)

	if moment.CoverMediaID != "first" {
		t.Fatalf("unexpected cover media ID: %q", moment.CoverMediaID)
	}
}

func TestGenerateSkipsLowDescriptionMatch(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	group := []Candidate{
		{ID: "first", CapturedAt: base},
		{ID: "second", CapturedAt: base.Add(time.Minute)},
		{ID: "third", CapturedAt: base.Add(2 * time.Minute)},
	}
	repository := &testRepository{candidates: group}
	service := New(repository, WithClusterer(testClusterer{groups: [][]Candidate{group}}), WithEnricher(testEnricher{confidence: 0.64}))
	router := testRouterWithService(service)
	request := httptest.NewRequest(http.MethodPost, "/moments/generate", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != `{"created":0}` || len(repository.created) != 0 {
		t.Fatalf("unexpected low-match response %d: %s", response.Code, response.Body.String())
	}
}

func TestGenerateSkipsGroupsWithFewerThanThreeImages(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	group := []Candidate{
		{ID: "first", CapturedAt: base},
		{ID: "second", CapturedAt: base.Add(time.Minute)},
	}
	repository := &testRepository{candidates: group}
	service := New(repository, WithClusterer(testClusterer{groups: [][]Candidate{group}}), WithEnricher(testEnricher{confidence: 1}))
	router := testRouterWithService(service)
	request := httptest.NewRequest(http.MethodPost, "/moments/generate", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != `{"created":0}` || len(repository.created) != 0 {
		t.Fatalf("unexpected small-group response %d: %s", response.Code, response.Body.String())
	}
}

func TestSemanticCandidatesSamplesAcrossGroup(t *testing.T) {
	candidates := make([]Candidate, 20)
	for index := range candidates {
		candidates[index].ID = string(rune('a' + index))
	}

	selected := semanticCandidates(candidates)
	if len(selected) != MaximumSemanticImages || selected[0].ID != "a" || selected[len(selected)-1].ID != "t" {
		t.Fatalf("unexpected semantic candidates: %#v", selected)
	}
}

func testRouter(repository Repository) *gin.Engine {
	return testRouterWithService(New(repository))
}

func testRouterWithService(service *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(ctx *gin.Context) {
		ctx.Set(auth.UserContextKey, auth.User{ID: "user-1", Role: "user"})
	})
	RegisterRoutes(router, service)
	return router
}
