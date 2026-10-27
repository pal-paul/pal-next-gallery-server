package moments

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type workerRepository struct {
	testRepository
	job                              DescriptionJob
	photos                           []EventPhoto
	claims, completed, failed, limit int
	savedGroups                      int
	newGroups                        int
	failSave                         bool
}

func (repository *workerRepository) ClaimMomentJob(_ context.Context, limit int) (DescriptionJob, bool, error) {
	repository.claims++
	repository.limit = limit
	return repository.job, repository.job.ID != "", nil
}

func (repository *workerRepository) CompleteMomentJob(context.Context, DescriptionJob) error {
	repository.completed++
	return nil
}

func (repository *workerRepository) FailMomentJob(context.Context, DescriptionJob, string) error {
	repository.failed++
	return nil
}

func (repository *workerRepository) EnqueueMomentCandidates(context.Context, string) (int64, error) {
	return 1, nil
}

func (repository *workerRepository) ListEventPhotos(context.Context, DescriptionJob) ([]EventPhoto, error) {
	return repository.photos, nil
}

func (repository *workerRepository) SaveEventGroup(_ context.Context, moment Moment, candidates []Candidate, isNew bool) error {
	if repository.failSave {
		return errors.New("save failed")
	}
	repository.savedGroups++
	if isNew {
		repository.newGroups++
	}
	repository.created = append(repository.created, moment)
	repository.groups = append(repository.groups, candidates)
	return nil
}

func eventPhotos(count int) []EventPhoto {
	photos := make([]EventPhoto, count)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for index := range photos {
		photos[index] = EventPhoto{Candidate: Candidate{ID: fmt.Sprintf("photo-%02d", index), CapturedAt: base.Add(time.Duration(index) * time.Minute), ReliableCapturedAt: true},
			Description: ImageDescription{Description: "Family picnic by the lake", Model: "gemini"}}
	}
	return photos
}

func TestWorkerDisabledDoesNotClaimOrCallGemini(t *testing.T) {
	for _, settings := range []struct {
		enabled bool
		limit   int
	}{{false, 100}, {true, 0}} {
		repository := &workerRepository{job: DescriptionJob{Candidate: Candidate{ID: "photo"}}}
		enricher := &testTwoStageEnricher{}
		processed, err := NewWorker(repository, enricher, enricher, settings.enabled, settings.limit).ProcessOnce(context.Background())
		if err != nil || processed || repository.claims != 0 || len(enricher.described) != 0 || len(enricher.synthesized) != 0 {
			t.Fatalf("disabled worker performed work: %v, %v, %+v", processed, err, repository)
		}
	}
}

func TestGenerateQueuesWithoutCallingGemini(t *testing.T) {
	repository := &workerRepository{}
	enricher := &testTwoStageEnricher{}
	service := New(repository, WithImageDescriber(enricher), WithMetadataSynthesizer(enricher))
	response := httptest.NewRecorder()
	testRouterWithService(service).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/moments/generate", nil))
	if response.Code != http.StatusAccepted || response.Body.String() != `{"created":0,"queued":1}` {
		t.Fatalf("unexpected enqueue response: %d %s", response.Code, response.Body.String())
	}
	if len(enricher.described) != 0 || len(enricher.synthesized) != 0 {
		t.Fatal("HTTP request performed inference")
	}
}

func TestWorkerPromotesFivePhotosAndRemovesJob(t *testing.T) {
	for _, count := range []int{4, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			photos := eventPhotos(count)
			repository := &workerRepository{photos: photos, job: DescriptionJob{Candidate: photos[count-1].Candidate, OwnerID: "owner"}}
			enricher := &testTwoStageEnricher{}
			processed, err := NewWorker(repository, enricher, enricher, true, 100).ProcessOnce(context.Background())
			if err != nil || !processed || repository.limit != 100 || repository.completed != 1 || len(repository.descriptions) != 1 {
				t.Fatalf("unexpected processing: %v, %v, %+v", processed, err, repository)
			}
			if count == 4 && (repository.savedGroups != 0 || len(enricher.synthesized) != 0) {
				t.Fatal("promoted fewer than five photos")
			}
			if count == 5 && (repository.newGroups != 1 || len(enricher.synthesized) != 5) {
				t.Fatal("five-photo event was not named and saved")
			}
		})
	}
}

func TestWorkerLatePhotoPreservesExistingSummary(t *testing.T) {
	photos := eventPhotos(6)
	for index := 0; index < 5; index++ {
		photos[index].MomentID = "confirmed"
	}
	repository := &workerRepository{photos: photos, job: DescriptionJob{Candidate: photos[5].Candidate, OwnerID: "owner", DescriptionReady: true}}
	repository.stored = []ImageDescription{photos[5].Description}
	enricher := &testTwoStageEnricher{}
	_, err := NewWorker(repository, enricher, enricher, true, 100).ProcessOnce(context.Background())
	if err != nil || repository.savedGroups != 1 || repository.newGroups != 0 || len(enricher.synthesized) != 0 || len(enricher.described) != 0 {
		t.Fatalf("unexpected late assignment: %v, %+v", err, repository)
	}
	if repository.created[0].ID != "confirmed" || len(repository.groups[0]) != 1 {
		t.Fatal("late photo did not join the confirmed event")
	}
}

func TestWorkerFailureRetainsJobForRetry(t *testing.T) {
	repository := &workerRepository{job: DescriptionJob{Candidate: Candidate{ID: "bad"}}}
	enricher := &testTwoStageEnricher{failID: "bad"}
	processed, err := NewWorker(repository, enricher, enricher, true, 100).ProcessOnce(context.Background())
	if !processed || err == nil || repository.completed != 0 || repository.failed != 1 {
		t.Fatalf("unexpected failed job handling: %v, %v, %+v", processed, err, repository)
	}
}

type testTextEnricher struct {
	testTwoStageEnricher
	embeddings    int
	failEmbedding bool
}

func (enricher *testTextEnricher) TextEmbeddingModel() string { return "text-model:768" }

func (enricher *testTextEnricher) EmbedDescription(_ context.Context, description ImageDescription) ([]float64, error) {
	enricher.embeddings++
	if enricher.failEmbedding {
		return nil, errors.New("embedding unavailable")
	}
	if description.Description == "" {
		return nil, errors.New("missing cached description")
	}
	return []float64{1, 0}, nil
}

func TestWorkerEmbeddingFailureRetriesWithoutVision(t *testing.T) {
	photos := eventPhotos(1)
	repository := &workerRepository{photos: photos, job: DescriptionJob{Candidate: photos[0].Candidate}}
	enricher := &testTextEnricher{failEmbedding: true}
	worker := NewWorker(repository, enricher, enricher, true, 100)
	if _, err := worker.ProcessOnce(context.Background()); err == nil || repository.failed != 1 || repository.completed != 0 || len(repository.descriptions) != 1 {
		t.Fatalf("embedding failure did not preserve the description/job: %v", err)
	}
	repository.stored = repository.descriptions
	repository.job.DescriptionReady = true
	enricher.failEmbedding = false
	if _, err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(enricher.described) != 1 || enricher.embeddings != 2 || repository.completed != 1 || len(repository.descriptions) != 2 {
		t.Fatal("retry repeated vision inference or lost the job")
	}
	stored := repository.descriptions[1]
	if stored.EmbeddingModel != enricher.TextEmbeddingModel() || len(stored.Embedding) != 2 {
		t.Fatal("embedding not cached")
	}
}

func TestWorkerEmbeddingCacheAndDisableGuards(t *testing.T) {
	for _, scenario := range []string{"cached", "changed model", "disabled", "zero limit"} {
		t.Run(scenario, func(t *testing.T) {
			photos := eventPhotos(1)
			enricher := &testTextEnricher{}
			description := photos[0].Description
			description.Embedding, description.EmbeddingModel = []float64{1, 0}, enricher.TextEmbeddingModel()
			if scenario == "changed model" {
				description.EmbeddingModel = "older-model"
			}
			repository := &workerRepository{photos: photos, job: DescriptionJob{Candidate: photos[0].Candidate, DescriptionReady: true}}
			repository.stored = []ImageDescription{description}
			limit := 100
			if scenario == "zero limit" {
				limit = 0
			}
			if _, err := NewWorker(repository, enricher, enricher, scenario != "disabled", limit).ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "changed model" {
				want = 1
			}
			if enricher.embeddings != want || len(enricher.described) != 0 {
				t.Fatal("unexpected embedding/vision request")
			}
			if (scenario == "disabled" || scenario == "zero limit") && repository.claims != 0 {
				t.Fatal("disabled worker claimed a job")
			}
		})
	}
}

func TestWorkerNamingFailureReusesCachedDescription(t *testing.T) {
	photos := eventPhotos(5)
	repository := &workerRepository{photos: photos, failSave: true, job: DescriptionJob{Candidate: photos[4].Candidate, DescriptionReady: true}}
	repository.stored = []ImageDescription{photos[4].Description}
	enricher := &testTwoStageEnricher{}
	_, err := NewWorker(repository, enricher, enricher, true, 100).ProcessOnce(context.Background())
	if err == nil || repository.failed != 1 || repository.completed != 0 || len(enricher.described) != 0 {
		t.Fatalf("failed persistence did not retain cached work: %v, %+v", err, repository)
	}
}

func TestEventClusteringDeduplicatesPhotoIDs(t *testing.T) {
	photos := eventPhotos(4)
	photos = append(photos, photos[0])
	groups := clusterEvents(photos)
	if len(groups) != 1 || len(groups[0].photos) != 4 {
		t.Fatal("repeated IDs counted as distinct photos")
	}
}

func TestDescriptorEmbeddingsUseCompatibleTextVectors(t *testing.T) {
	left := ImageDescription{Description: "seaside swimming", EmbeddingModel: "text-model", Embedding: []float64{1, 0}}
	right := ImageDescription{Description: "ocean holiday", EmbeddingModel: "text-model", Embedding: []float64{0.9, 0.1}}
	if score := descriptionSimilarity(left, right); score < 0.99 {
		t.Fatalf("semantic paraphrases were not matched: %f", score)
	}
	for _, vector := range [][]float64{nil, {0, 0}, {1}, {math.NaN(), 1}, {math.Inf(1), 0}} {
		right.Embedding = vector
		if score := descriptionSimilarity(left, right); score != 0 {
			t.Fatalf("invalid vector did not fall back to tokens: %f", score)
		}
	}
	right.Embedding, right.EmbeddingModel = []float64{1, 0}, "pixel-model"
	if descriptionSimilarity(left, right) != 0 {
		t.Fatal("compared incompatible model identities")
	}
	right.EmbeddingModel, right.Embedding = "text-model", []float64{-1, 0}
	if descriptionSimilarity(left, right) != 0 {
		t.Fatal("negative cosine was not clamped")
	}
}

func TestEventClusteringRejectsMissingEvidenceAndSeparateTrips(t *testing.T) {
	for _, scenario := range []string{"missing metadata", "separate trips", "different descriptions", "distant GPS"} {
		t.Run(scenario, func(t *testing.T) {
			photos := eventPhotos(2)
			switch scenario {
			case "missing metadata":
				photos[0].ReliableCapturedAt, photos[1].ReliableCapturedAt = false, false
			case "separate trips":
				photos[1].CapturedAt = photos[0].CapturedAt.Add(7 * 24 * time.Hour)
			case "different descriptions":
				photos[1].Description.Description = "Office meeting conference presentation"
			case "distant GPS":
				latitude, nearLongitude, farLongitude := 0.0, 0.0, 2.0
				photos[0].Latitude, photos[0].Longitude = &latitude, &nearLongitude
				photos[1].Latitude, photos[1].Longitude = &latitude, &farLongitude
			}
			if groups := clusterEvents(photos); len(groups) != 2 {
				t.Fatalf("unexpected merge: %+v", groups)
			}
		})
	}
}

func TestEventClusteringAmbiguityAndDeterminism(t *testing.T) {
	photos := eventPhotos(3)
	photos[0].MomentID, photos[2].MomentID = "left", "right"
	groups := clusterEvents(photos)
	if len(groups) != 3 {
		t.Fatal("bridged competing confirmed moments")
	}
	photos[0], photos[2] = photos[2], photos[0]
	if !reflect.DeepEqual(groups, clusterEvents(photos)) {
		t.Fatal("clustering depends on input order")
	}
}

func TestEventScoreMatchesReferenceMissingEvidence(t *testing.T) {
	photos := eventPhotos(2)
	photos[1].CapturedAt = photos[0].CapturedAt.Add(time.Hour)
	left, right := eventGroup{photos: photos[:1]}, eventGroup{photos: photos[1:]}
	want := (0.45 + 0.30*math.Exp(-1.0/6)) / 0.75
	if got := eventScore(left, right); math.Abs(got-want) > 1e-12 {
		t.Fatalf("time-only score = %f, want %f", got, want)
	}
	photos[1].ReliableCapturedAt = false
	if got := eventScore(left, right); got != 0 {
		t.Fatalf("description alone established an event: %f", got)
	}
	latitude, longitude := 10.0, 20.0
	for index := range photos {
		photos[index].Latitude, photos[index].Longitude = &latitude, &longitude
	}
	if got := eventScore(left, right); math.Abs(got-1) > 1e-12 {
		t.Fatalf("GPS-only corroboration score = %f, want 1", got)
	}
}

func TestEventScoreMatchesReferenceContradictoryEvidence(t *testing.T) {
	photos := eventPhotos(2)
	latitude, longitude := 10.0, 20.0
	for index := range photos {
		photos[index].Latitude, photos[index].Longitude = &latitude, &longitude
	}
	photos[1].CapturedAt = photos[0].CapturedAt.Add(13 * time.Hour)
	if score := eventScore(eventGroup{photos: photos[:1]}, eventGroup{photos: photos[1:]}); score != 0 {
		t.Fatal("identical GPS rescued a hard time rejection")
	}
	photos[1].CapturedAt = photos[0].CapturedAt.Add(5 * time.Hour)
	photos[0].Latitude, photos[1].Latitude = nil, nil
	if score := eventScore(eventGroup{photos: photos[:1]}, eventGroup{photos: photos[1:]}); score != 0 {
		t.Fatal("weak time evidence passed corroboration")
	}
}

func TestEventSpanMatchesReferencePreventsChaining(t *testing.T) {
	photos := eventPhotos(3)
	photos[0].MomentID, photos[1].MomentID = "existing", "existing"
	photos[1].CapturedAt = photos[0].CapturedAt.Add(47 * time.Hour)
	photos[2].CapturedAt = photos[0].CapturedAt.Add(48*time.Hour + 30*time.Minute)
	if groups := clusterEvents(photos); len(groups) != 2 {
		t.Fatal("a nearby latest photo extended an event past the maximum span")
	}
}

func TestEventRepresentativesMatchReference(t *testing.T) {
	photos := eventPhotos(40)
	representatives := eventRepresentatives(photos)
	if len(representatives) != 20 || representatives[0].ID != photos[0].ID || representatives[19].ID != photos[39].ID {
		t.Fatal("representatives must evenly sample the entire capture-time range")
	}
	for index, representative := range representatives {
		if representative.ID != photos[index*39/19].ID {
			t.Fatalf("representative %d differs from reference sampling", index)
		}
	}
}

func TestEventDistanceRemainsFiniteForAntipodalCoordinates(t *testing.T) {
	for index := 0; index < 400; index++ {
		latitude := -89.0 + float64(index)*0.37
		distance := coordinateDistanceKM(latitude, 13.7, -latitude, -166.3)
		if math.IsNaN(distance) || math.IsInf(distance, 0) || math.Abs(distance-6371*math.Pi) > 0.001 {
			t.Fatalf("invalid antipodal distance at latitude %f: %f", latitude, distance)
		}
	}
}
