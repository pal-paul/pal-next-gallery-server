package moments

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
)

type testRepository struct {
	owners       []string
	candidates   []Candidate
	created      []Moment
	groups       [][]Candidate
	updated      Moment
	descriptions []ImageDescription
	stored       []ImageDescription
}

type testClusterer struct{ groups [][]Candidate }

func (clusterer testClusterer) Cluster(context.Context, []Candidate) ([][]Candidate, error) {
	return clusterer.groups, nil
}

type testEnricher struct{ confidence float64 }

func (enricher testEnricher) Enrich(context.Context, Moment, []Candidate) (Metadata, error) {
	return Metadata{Title: "A day by the water", Description: "Sunny outdoor photos.", Confidence: enricher.confidence}, nil
}

type testTwoStageEnricher struct {
	described   []string
	synthesized []ImageDescription
	mergeGroups [][]int
}

func (enricher *testTwoStageEnricher) Describe(_ context.Context, candidate Candidate) (ImageDescription, error) {
	enricher.described = append(enricher.described, candidate.ID)
	return ImageDescription{Model: "vision", Description: "Description of " + candidate.ID}, nil
}

func (enricher *testTwoStageEnricher) Synthesize(_ context.Context, _ Moment, descriptions []ImageDescription) (Metadata, error) {
	enricher.synthesized = append(enricher.synthesized, descriptions...)
	return Metadata{Title: "Synthesized title", Description: "Synthesized description", Confidence: 0.9}, nil
}

func (enricher *testTwoStageEnricher) MergeClusters(_ context.Context, clusters [][]ImageDescription) ([][]int, error) {
	if enricher.mergeGroups != nil {
		return enricher.mergeGroups, nil
	}
	groups := make([][]int, len(clusters))
	for index := range clusters {
		groups[index] = []int{index}
	}
	return groups, nil
}

func (repository *testRepository) ListMomentOwners(context.Context) ([]string, error) {
	return repository.owners, nil
}
func (repository *testRepository) ListMoments(context.Context, string) ([]Moment, error) {
	return repository.created, nil
}
func (repository *testRepository) GetMoment(_ context.Context, id, ownerID string) (Moment, error) {
	for _, moment := range repository.created {
		if moment.ID == id && moment.OwnerID == ownerID {
			return moment, nil
		}
	}
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
func (repository *testRepository) ListImageDescriptions(context.Context, []string) ([]ImageDescription, error) {
	return repository.stored, nil
}
func (repository *testRepository) SaveImageDescriptions(_ context.Context, descriptions []ImageDescription) error {
	repository.descriptions = append(repository.descriptions, descriptions...)
	return nil
}
func (repository *testRepository) CreateGeneratedMoment(_ context.Context, moment Moment, candidates []Candidate) error {
	repository.created = append(repository.created, moment)
	repository.groups = append(repository.groups, candidates)
	return nil
}
func (repository *testRepository) CreateMoment(_ context.Context, moment Moment, _ []string) error {
	repository.created = append(repository.created, moment)
	return nil
}
func (repository *testRepository) ListMomentMetadataCandidates(context.Context, string, string, int) ([]Candidate, error) {
	return repository.candidates, nil
}
func (repository *testRepository) ListAlbumMetadataCandidates(context.Context, string, string, int) ([]Candidate, error) {
	return repository.candidates, nil
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

func TestCreateMakesManualDraft(t *testing.T) {
	repository := &testRepository{}
	router := testRouter(repository)
	request := httptest.NewRequest(http.MethodPost, "/moments",
		bytes.NewBufferString(`{"title":"  Weekend walk  ","description":"  Near the lake  ","mediaIds":["one","two"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || len(repository.created) != 1 {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
	created := repository.created[0]
	if created.OwnerID != "user-1" || created.Title != "Weekend walk" || created.Description != "Near the lake" || created.Status != "draft" {
		t.Fatalf("unexpected manual moment: %#v", created)
	}
}

func TestAIFeaturesCanBeDisabled(t *testing.T) {
	repository := &testRepository{}
	router := testRouterWithService(New(repository, WithAIFeaturesEnabled(false)))
	for _, path := range []string{"/moments/generate", "/moments/moment-1/metadata-suggestion", "/albums/album-1/metadata-suggestion"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected disabled response for %s, got %d: %s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/features", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"ai":false}` {
		t.Fatalf("unexpected features response %d: %s", response.Code, response.Body.String())
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

func TestGenerateDescribesPersistsAndSynthesizesSelectedImages(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	group := []Candidate{
		{ID: "first", CapturedAt: base, Representative: true},
		{ID: "second", CapturedAt: base.Add(time.Minute), Representative: true},
		{ID: "third", CapturedAt: base.Add(2 * time.Minute), Representative: true},
	}
	repository := &testRepository{candidates: group}
	enricher := &testTwoStageEnricher{}
	service := New(repository, WithClusterer(testClusterer{groups: [][]Candidate{group}}),
		WithImageDescriber(enricher), WithMetadataSynthesizer(enricher))
	router := testRouterWithService(service)
	request := httptest.NewRequest(http.MethodPost, "/moments/generate", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != `{"created":1}` {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
	if len(enricher.described) != 3 || len(repository.descriptions) != 3 || len(enricher.synthesized) != 3 {
		t.Fatalf("unexpected pipeline calls: described=%v persisted=%v synthesized=%v",
			enricher.described, repository.descriptions, enricher.synthesized)
	}
	for index, description := range repository.descriptions {
		if description.MediaID != group[index].ID {
			t.Fatalf("description %d has media ID %q", index, description.MediaID)
		}
	}
	if repository.created[0].Title != "Synthesized title" || repository.created[0].Description != "Synthesized description" {
		t.Fatalf("unexpected synthesized moment: %#v", repository.created[0])
	}
}

func TestGenerateSemanticallyMergesFragmentedVisualClusters(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	visualGroups := [][]Candidate{
		{{ID: "home-1", CapturedAt: base, Representative: true}, {ID: "home-2", CapturedAt: base}, {ID: "home-3", CapturedAt: base}},
		{{ID: "exhibit-1", CapturedAt: base, Representative: true}, {ID: "exhibit-2", CapturedAt: base}},
		{{ID: "garden-1", CapturedAt: base, Representative: true}, {ID: "garden-2", CapturedAt: base}, {ID: "garden-3", CapturedAt: base}},
		{{ID: "exhibit-3", CapturedAt: base, Representative: true}},
	}
	repository := &testRepository{candidates: append(append(append(visualGroups[0], visualGroups[1]...), visualGroups[2]...), visualGroups[3]...)}
	enricher := &testTwoStageEnricher{mergeGroups: [][]int{{0}, {1, 3}, {2}}}
	service := New(repository, WithClusterer(testClusterer{groups: visualGroups}),
		WithImageDescriber(enricher), WithMetadataSynthesizer(enricher))

	created, err := service.generateForOwner(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if created != 3 || len(repository.groups) != 3 {
		t.Fatalf("unexpected generated groups: %#v", repository.groups)
	}
	if len(repository.groups[1]) != 3 || repository.groups[1][0].ID != "exhibit-1" || repository.groups[1][2].ID != "exhibit-3" {
		t.Fatalf("fragmented event was not merged: %#v", repository.groups[1])
	}
	if len(repository.descriptions) != 4 {
		t.Fatalf("expected each representative to be described once, got %d descriptions", len(repository.descriptions))
	}
}

func TestGenerateFallsBackFromInvalidSemanticPartition(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	visualGroups := [][]Candidate{
		{{ID: "first-1", CapturedAt: base, Representative: true}, {ID: "first-2", CapturedAt: base}, {ID: "first-3", CapturedAt: base}},
		{{ID: "second-1", CapturedAt: base, Representative: true}, {ID: "second-2", CapturedAt: base}, {ID: "second-3", CapturedAt: base}},
	}
	repository := &testRepository{candidates: append(visualGroups[0], visualGroups[1]...)}
	enricher := &testTwoStageEnricher{mergeGroups: [][]int{{0}, {2}}}
	service := New(repository, WithClusterer(testClusterer{groups: visualGroups}),
		WithImageDescriber(enricher), WithMetadataSynthesizer(enricher))

	created, err := service.generateForOwner(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if created != 2 || len(repository.groups) != 2 {
		t.Fatalf("invalid semantic partition did not fall back to visual groups: %#v", repository.groups)
	}
}

func TestGenerateReusesStoredImageDescriptions(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	group := []Candidate{
		{ID: "stored", CapturedAt: base, Representative: true},
		{ID: "second", CapturedAt: base.Add(time.Minute)},
		{ID: "third", CapturedAt: base.Add(2 * time.Minute)},
	}
	repository := &testRepository{
		candidates: group,
		stored:     []ImageDescription{{MediaID: "stored", Model: "vision", Description: "Cached description"}},
	}
	enricher := &testTwoStageEnricher{}
	service := New(repository, WithClusterer(testClusterer{groups: [][]Candidate{group}}),
		WithImageDescriber(enricher), WithMetadataSynthesizer(enricher))

	created, err := service.generateForOwner(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || len(enricher.described) != 0 || len(repository.descriptions) != 0 {
		t.Fatalf("stored description was not reused: created=%d described=%v saved=%v",
			created, enricher.described, repository.descriptions)
	}
	if len(enricher.synthesized) != 1 || enricher.synthesized[0].Description != "Cached description" {
		t.Fatalf("unexpected synthesized descriptions: %#v", enricher.synthesized)
	}
}

func TestMergeCompatiblePublicVenueGroupsPreservesEventBoundaries(t *testing.T) {
	base := time.Date(2026, time.July, 12, 9, 30, 0, 0, time.UTC)
	groups := [][]Candidate{
		{{ID: "home", CapturedAt: base}},
		{{ID: "statues", CapturedAt: base.Add(time.Minute)}},
		{{ID: "garden", CapturedAt: base.Add(2 * time.Minute)}},
		{{ID: "atrium", CapturedAt: base.Add(3 * time.Minute)}},
		{{ID: "detail", CapturedAt: base.Add(4 * time.Minute)}},
	}
	descriptions := [][]ImageDescription{
		{{LocationType: "indoor", Scene: "living room", Objects: []string{"sofa"}}},
		{{LocationType: "indoor exhibition", Scene: "statue display with a red decorative background and hanging rings"}},
		{{LocationType: "outdoor", Scene: "garden with flowers"}},
		{{LocationType: "indoor", Scene: "building atrium with storefronts"}},
		{{LocationType: "indoor", Scene: "red background with hanging lantern", Objects: []string{"lantern", "circular decorations"}}},
	}

	merged := mergeCompatibleEventContextGroups([][]int{{0}, {1}, {2}, {3}, {4}}, groups, descriptions)

	want := [][]int{{0}, {1, 3, 4}, {2}}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("unexpected reconciled partition: got %#v want %#v", merged, want)
	}
}

func TestClassifyEventContextPreservesUnknown(t *testing.T) {
	tests := []struct {
		name        string
		description ImageDescription
		want        eventContext
	}{
		{name: "public attraction outdoors", description: ImageDescription{LocationType: "outdoor zoo"}, want: contextPublicVenue},
		{name: "outdoor activity", description: ImageDescription{Scene: "family picnic in a park"}, want: contextOutdoorActivity},
		{name: "private birthday", description: ImageDescription{Scene: "birthday party in our home living room"}, want: contextHomeOrPrivate},
		{name: "event", description: ImageDescription{Scene: "school graduation ceremony"}, want: contextEventCelebration},
		{name: "travel", description: ImageDescription{LocationType: "airport terminal"}, want: contextTravel},
		{name: "unknown detail", description: ImageDescription{Scene: "red background with hanging lantern"}, want: contextUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyEventContext([]ImageDescription{test.description}); got != test.want {
				t.Fatalf("classifyEventContext() = %q, want %q", got, test.want)
			}
		})
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
