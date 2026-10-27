package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"pal-next-gallery-server/app/auth"
	"pal-next-gallery-server/app/moments"
	"pal-next-gallery-server/app/processing"
	"pal-next-gallery-server/app/uploader"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresConcurrentImportsRespectStorageQuota(t *testing.T) {
	store, ownerID, _ := momentTestStore(t, 0)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `UPDATE users SET storage_quota_bytes = 10 WHERE id = $1`, ownerID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 3)
	for index := 0; index < 3; index++ {
		mediaID := uuid.NewString()
		go func() {
			<-start
			results <- store.SaveCompletedMedia(ctx, uploader.CompletedMedia{
				UploadID: mediaID, OwnerID: ownerID, Filename: "photo.jpg", MimeType: "image/jpeg",
				Size: 6, SHA256: mediaID, MediaPath: mediaID + ".jpg", CreatedAt: time.Now().UTC(),
			})
		}()
	}
	close(start)
	saved := 0
	for index := 0; index < 3; index++ {
		if err := <-results; err == nil {
			saved++
		} else if !errors.Is(err, uploader.ErrStorageQuota) {
			t.Errorf("unexpected save error: %v", err)
		}
	}
	if saved != 1 {
		t.Fatalf("saved %d concurrent imports; only one fits the quota", saved)
	}
}

func TestPostgresImportsRespectPendingUploads(t *testing.T) {
	store, ownerID, _ := momentTestStore(t, 0)
	ctx := context.Background()
	upload := uploader.Upload{ID: uuid.NewString(), OwnerID: ownerID, Size: 6}
	if err := store.ReserveUploadStorage(ctx, upload, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreUploadStorage(ctx, upload); err != nil {
		t.Fatal(err)
	}
	media := uploader.CompletedMedia{
		UploadID: uuid.NewString(), OwnerID: ownerID, Filename: "photo.jpg", MimeType: "image/jpeg",
		Size: 5, SHA256: "import", MediaPath: "import.jpg", CreatedAt: time.Now().UTC(), StorageQuota: 10,
	}
	if err := store.SaveCompletedMedia(ctx, media); !errors.Is(err, uploader.ErrStorageQuota) {
		t.Fatalf("import ignored pending upload reservation: %v", err)
	}
	media.Size = 4
	if err := store.SaveCompletedMedia(ctx, media); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveUploadStorage(ctx, uploader.Upload{ID: uuid.NewString(), OwnerID: ownerID, Size: 1}, 10); !errors.Is(err, uploader.ErrStorageQuota) {
		t.Fatalf("upload ignored imported media: %v", err)
	}
	media.UploadID, media.SHA256, media.MediaPath, media.Size = upload.ID, "upload", "upload.jpg", upload.Size
	if err := store.SaveCompletedMedia(ctx, media); err != nil {
		t.Fatalf("completion counted its own reservation twice: %v", err)
	}
	var reservations int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM upload_storage_reservations WHERE owner_id = $1`, ownerID).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("completion retained %d reservations", reservations)
	}
	if err := store.SaveCompletedMedia(ctx, media); err != nil {
		t.Fatalf("idempotent completion counted stored media twice: %v", err)
	}
}

func TestPostgresConcurrentImportsAndUploadReservations(t *testing.T) {
	store, ownerID, _ := momentTestStore(t, 0)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 12)
	for index := 0; index < 12; index++ {
		mediaID := uuid.NewString()
		reserve := index%2 == 0
		go func() {
			<-start
			if reserve {
				results <- store.ReserveUploadStorage(ctx, uploader.Upload{ID: mediaID, OwnerID: ownerID, Size: 6}, 10)
				return
			}
			results <- store.SaveCompletedMedia(ctx, uploader.CompletedMedia{
				UploadID: mediaID, OwnerID: ownerID, Filename: "photo.jpg", MimeType: "image/jpeg",
				Size: 6, SHA256: mediaID, MediaPath: mediaID + ".jpg", CreatedAt: time.Now().UTC(), StorageQuota: 10,
			})
		}()
	}
	close(start)
	saved := 0
	for index := 0; index < 12; index++ {
		if err := <-results; err == nil {
			saved++
		} else if !errors.Is(err, uploader.ErrStorageQuota) {
			t.Errorf("unexpected quota error: %v", err)
		}
	}
	if saved != 1 {
		t.Fatalf("accepted %d concurrent imports/reservations; only one fits", saved)
	}
}

func TestPostgresRestoresAndReleasesUploadStorage(t *testing.T) {
	store, ownerID, _ := momentTestStore(t, 0)
	ctx := context.Background()
	upload := uploader.Upload{ID: uuid.NewString(), OwnerID: ownerID, Size: 12}
	if _, err := store.pool.Exec(ctx, `UPDATE users SET storage_quota_bytes = 10 WHERE id = $1`, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreUploadStorage(ctx, upload); err != nil {
		t.Fatalf("could not restore an upload after a quota reduction: %v", err)
	}
	if err := store.ReserveUploadStorage(ctx, uploader.Upload{ID: uuid.NewString(), OwnerID: ownerID, Size: 1}, 100); !errors.Is(err, uploader.ErrStorageQuota) {
		t.Fatalf("restored upload did not reserve storage: %v", err)
	}
	if err := store.ReleaseUploadStorage(ctx, ownerID, upload.ID); err != nil {
		t.Fatal(err)
	}
	upload.ID, upload.Size = uuid.NewString(), 10
	if err := store.ReserveUploadStorage(ctx, upload, 100); err != nil {
		t.Fatalf("released storage was not available: %v", err)
	}
	if err := store.ReserveUploadStorage(ctx, uploader.Upload{ID: uuid.NewString(), OwnerID: ownerID, Size: 1}, 100); !errors.Is(err, uploader.ErrStorageQuota) {
		t.Fatalf("per-user quota did not override the default: %v", err)
	}
}

func TestSchemaUpgradesExistingAlbumsTable(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS automatic",
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS auto_key",
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS position",
		"ALTER TABLE albums ADD COLUMN IF NOT EXISTS cover_media_id",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("schema does not contain %q", statement)
		}
	}
}

func TestSchemaUpgradesSharesWithReadPermission(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE user_media_shares ADD COLUMN IF NOT EXISTS permission TEXT NOT NULL DEFAULT 'read'",
		"CHECK (permission IN ('read', 'write'))",
		"trash_retention_days INTEGER NOT NULL DEFAULT 30",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("schema does not contain %q", statement)
		}
	}
}

func TestSharedTrashRequiresWritePermission(t *testing.T) {
	if !strings.Contains(trashMediaQuery, "share.permission = 'write'") {
		t.Fatal("shared trash query does not require write permission")
	}
}

func TestGalleryUsesCaptureTimeWithUploadFallback(t *testing.T) {
	effectiveDate := "COALESCE(exif.captured_at, m.created_at)"
	if !strings.Contains(mediaSelect, effectiveDate) {
		t.Fatal("gallery media date does not prefer capture time")
	}
}

func TestAlbumListFallsBackToNewestAccessibleMediaForCover(t *testing.T) {
	for _, fragment := range []string{
		"ARRAY_AGG(m.upload_id ORDER BY COALESCE(exif.captured_at, m.created_at) DESC",
		"FILTER (WHERE m.deleted_at IS NULL",
		"LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id",
	} {
		if !strings.Contains(listAlbumsQuery, fragment) {
			t.Errorf("album list query does not contain %q", fragment)
		}
	}
}

func TestSchemaCreatesMomentsSeparatelyFromAlbums(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS moments",
		"CREATE TABLE IF NOT EXISTS moment_media",
		"REFERENCES moments(id) ON DELETE CASCADE",
		"REFERENCES media_uploads(upload_id) ON DELETE CASCADE",
		"user_edited BOOLEAN NOT NULL DEFAULT FALSE",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("moments schema does not contain %q", statement)
		}
	}
}

func TestMomentCandidatesRequireMediaOptIn(t *testing.T) {
	if !strings.Contains(schema, "moments_enabled BOOLEAN NOT NULL DEFAULT TRUE") {
		t.Fatal("schema does not add moments_enabled with a backward-compatible default")
	}
	if !strings.Contains(momentCandidatesQuery, "media.moments_enabled = TRUE") {
		t.Fatal("moment candidates do not enforce media opt-in")
	}
}

func TestSchemaCreatesTakeoutAlbumMappings(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS takeout_albums",
		"PRIMARY KEY (owner_id, source_key)",
		"album_id UUID NOT NULL UNIQUE REFERENCES albums(id) ON DELETE CASCADE",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("Takeout schema does not contain %q", statement)
		}
	}
}

func TestSchemaPersistsPreprocessingAnalysis(t *testing.T) {
	for _, statement := range []string{
		"camera_make TEXT NOT NULL DEFAULT ''",
		"camera_model TEXT NOT NULL DEFAULT ''",
		"orientation TEXT NOT NULL DEFAULT ''",
		"perceptual_hash TEXT NOT NULL DEFAULT ''",
		"media_exif_perceptual_hash_idx",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("preprocessing schema does not contain %q", statement)
		}
	}
}

func TestSchemaCreatesNearDuplicateGroups(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS media_duplicate_groups",
		"CREATE TABLE IF NOT EXISTS media_duplicate_group_members",
		"hamming_distance INTEGER NOT NULL",
		"media_duplicate_primary_idx",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("duplicate schema does not contain %q", statement)
		}
	}
}

func TestDifferenceHashDistance(t *testing.T) {
	distance, err := differenceHashDistance("0000000000000000", "0000000000000007")
	if err != nil {
		t.Fatal(err)
	}
	if distance != 3 {
		t.Fatalf("unexpected Hamming distance: %d", distance)
	}
}

func TestMomentsDoNotRequireLegacyImageEmbeddings(t *testing.T) {
	if strings.Contains(schema, "media_embeddings") {
		t.Error("new installations should not create the retired image-embedding table")
	}
	if strings.Contains(momentCandidatesQuery, "media_embeddings") {
		t.Error("moment candidates should not depend on retired image embeddings")
	}
}

func TestSchemaPersistsStructuredImageDescriptions(t *testing.T) {
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS media_image_descriptions",
		"people TEXT[] NOT NULL",
		"activities TEXT[] NOT NULL",
		"location_type TEXT NOT NULL",
		"weather TEXT NOT NULL",
		"PRIMARY KEY (upload_id, model, version)",
	} {
		if !strings.Contains(schema, statement) {
			t.Errorf("image description schema does not contain %q", statement)
		}
	}
}

func momentTestStore(t *testing.T, count int) (*Postgres, string, []moments.Candidate) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL must point to an isolated test database")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	schemaName := "moment_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := bootstrap.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schemaName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	store := &Postgres{pool: pool}
	t.Cleanup(store.Close)
	t.Logf("retaining test schema %s", schemaName)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ownerID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, username, password_hash) VALUES ($1, 'test', 'not-a-real-password')`, ownerID); err != nil {
		t.Fatal(err)
	}
	candidates := make([]moments.Candidate, count)
	for index := range candidates {
		id := fmt.Sprintf("photo-%02d", index)
		captured := time.Now().UTC().Add(-time.Hour).Add(time.Duration(index) * time.Minute)
		candidates[index] = moments.Candidate{ID: id, SourcePath: id + ".jpg", CapturedAt: captured, ReliableCapturedAt: true}
		if _, err := pool.Exec(ctx, `INSERT INTO media_uploads
			(upload_id, owner_id, filename, mime_type, size, sha256, media_path, created_at)
			VALUES ($1, $2, $1, 'image/jpeg', 1, $1, $1, $3)`, id, ownerID, captured); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_exif (upload_id, captured_at) VALUES ($1, $2)`, id, captured); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_processing_jobs (id, upload_id, status) VALUES ($1, $1, 'completed')`, id); err != nil {
			t.Fatal(err)
		}
	}
	return store, ownerID, candidates
}

func TestPostgresMomentCandidatesWithoutImageEmbeddings(t *testing.T) {
	store, ownerID, expected := momentTestStore(t, 3)
	candidates, err := store.ListMomentCandidates(context.Background(), ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(expected) {
		t.Fatalf("candidate count = %d, want %d", len(candidates), len(expected))
	}
	for index, candidate := range candidates {
		if candidate.ID != expected[index].ID {
			t.Errorf("unexpected candidate: %+v", candidate)
		}
	}
}

func TestPostgresMomentQueueBudgetAndLeases(t *testing.T) {
	store, ownerID, _ := momentTestStore(t, 4)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `UPDATE media_uploads SET moments_enabled = FALSE WHERE upload_id = 'photo-03'`); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.EnqueueMomentCandidates(ctx, ownerID); err != nil || queued != 3 {
		t.Fatalf("enqueue = %d, %v", queued, err)
	}
	if queued, err := store.EnqueueMomentCandidates(ctx, ownerID); err != nil || queued != 0 {
		t.Fatalf("repeated enqueue = %d, %v", queued, err)
	}
	type result struct {
		job   moments.DescriptionJob
		found bool
		err   error
	}
	results := make(chan result, 4)
	for attempt := 0; attempt < 4; attempt++ {
		go func() {
			job, found, err := store.ClaimMomentJob(ctx, 1)
			results <- result{job, found, err}
		}()
	}
	var claimed moments.DescriptionJob
	claims := 0
	for attempt := 0; attempt < 4; attempt++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.found {
			claimed = result.job
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("daily limit exceeded by concurrent workers: %d claims", claims)
	}
	description := moments.ImageDescription{MediaID: claimed.ID, Model: "gemini", Description: "Family picnic by the lake", People: []string{}, Activities: []string{}, Objects: []string{}}
	if err := store.SaveImageDescriptions(ctx, []moments.ImageDescription{description}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE moment_description_jobs SET lease_until = now() - interval '1 minute' WHERE upload_id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, found, err := store.ClaimMomentJob(ctx, 1)
	if err != nil || !found || !reclaimed.DescriptionReady || reclaimed.ID != claimed.ID {
		t.Fatalf("cached retry at budget limit = %+v, %v, %v", reclaimed, found, err)
	}
	if err := store.CompleteMomentJob(ctx, claimed); err == nil {
		t.Fatal("stale lease completed a reclaimed job")
	}
	if err := store.CompleteMomentJob(ctx, reclaimed); err != nil {
		t.Fatal(err)
	}
	var usage int
	if err := store.pool.QueryRow(ctx, `SELECT image_attempts FROM moment_daily_usage`).Scan(&usage); err != nil || usage != 1 {
		t.Fatalf("cached retry consumed quota: %d, %v", usage, err)
	}
	var queued bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM moment_description_jobs WHERE upload_id = $1)`, claimed.ID).Scan(&queued); err != nil || queued {
		t.Fatalf("completed job retained: %v, %v", queued, err)
	}
	stored, err := store.ListImageDescriptions(ctx, []string{claimed.ID})
	if err != nil || len(stored) != 1 {
		t.Fatal("completion removed the permanent description")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE moment_daily_usage SET usage_day = usage_day - 1 WHERE usage_day = (now() AT TIME ZONE 'UTC')::date`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ClaimMomentJob(ctx, 1); err != nil || !found {
		t.Fatalf("new UTC day did not resume work: %v, %v", found, err)
	}
}

func TestPostgresMomentBacklogDrainsAcrossDays(t *testing.T) {
	store, ownerID, candidates := momentTestStore(t, 10)
	ctx := context.Background()
	inference := &momentWorkflowInference{}
	for index := range candidates {
		candidates[index].CapturedAt = candidates[index].CapturedAt.AddDate(-2, 0, 0)
	}
	if _, err := store.pool.Exec(ctx, `WITH historical AS (
		UPDATE media_uploads SET created_at = created_at - INTERVAL '2 years'
		WHERE owner_id = $1 RETURNING upload_id, created_at)
		UPDATE media_exif SET captured_at = historical.created_at FROM historical
		WHERE media_exif.upload_id = historical.upload_id`, ownerID); err != nil {
		t.Fatal(err)
	}
	moment := moments.Moment{ID: uuid.NewString(), OwnerID: ownerID, Title: "Existing title", Description: "Existing description",
		StartTime: candidates[0].CapturedAt, EndTime: candidates[4].CapturedAt, CoverMediaID: candidates[0].ID}
	if err := store.SaveEventGroup(ctx, moment, candidates[:5], true); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 3; index++ {
		description := moments.ImageDescription{MediaID: candidates[index].ID, Model: "vision", Description: "Old photo description",
			People: []string{}, Activities: []string{}, Objects: []string{}}
		if index >= 2 {
			description.Embedding, description.EmbeddingModel = []float64{1, 0}, inference.TextEmbeddingModel()
		}
		if index == 3 {
			description.EmbeddingModel = "previous-text-model"
		}
		if err := store.SaveImageDescriptions(ctx, []moments.ImageDescription{description}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `UPDATE media_uploads SET deleted_at = now() WHERE upload_id = 'photo-04';
		UPDATE media_uploads SET mime_type = 'video/mp4' WHERE upload_id = 'photo-05';
		UPDATE media_uploads SET moments_enabled = FALSE WHERE upload_id = 'photo-06';
		UPDATE media_processing_jobs SET status = 'failed' WHERE upload_id = 'photo-07'`); err != nil {
		t.Fatal(err)
	}
	groupID := uuid.NewString()
	if _, err := store.pool.Exec(ctx, `INSERT INTO media_duplicate_groups (id, owner_id, kind, primary_upload_id)
		VALUES ($1, $2, 'near', 'photo-00')`, groupID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO media_duplicate_group_members (group_id, upload_id, hamming_distance, is_primary)
		VALUES ($1, 'photo-08', 0, FALSE)`, groupID); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.EnqueueMomentBacklog(ctx, inference.TextEmbeddingModel()); err != nil || queued != 4 {
		t.Fatalf("initial backlog = %d, %v", queued, err)
	}
	if queued, err := store.EnqueueMomentBacklog(ctx, inference.TextEmbeddingModel()); err != nil || queued != 0 {
		t.Fatalf("repeated discovery duplicated jobs: %d, %v", queued, err)
	}
	photos, err := store.ListEventPhotos(ctx, moments.DescriptionJob{Candidate: candidates[1], OwnerID: ownerID})
	if err != nil || len(photos) != 4 {
		t.Fatalf("confirmed moment lost uncached members: %d, %v", len(photos), err)
	}
	worker := moments.NewWorker(store, inference, inference, true, 1)
	for attempt := 0; attempt < 3; attempt++ {
		if processed, err := worker.ProcessOnce(ctx); err != nil || !processed {
			t.Fatalf("backlog job %d = %v, %v", attempt, processed, err)
		}
	}
	if processed, err := worker.ProcessOnce(ctx); err != nil || processed {
		t.Fatalf("daily quota did not pause the backlog: %v, %v", processed, err)
	}
	if inference.described != 1 || inference.embedded != 3 || inference.named != 0 {
		t.Fatalf("cached/confirmed photos repeated inference: %+v", inference)
	}
	if queued, err := store.EnqueueMomentBacklog(ctx, inference.TextEmbeddingModel()); err != nil || queued != 0 {
		t.Fatalf("restart requeued completed work: %d, %v", queued, err)
	}
	var pending int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM moment_description_jobs`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("paused backlog = %d, %v", pending, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE moment_daily_usage SET usage_day = usage_day - 1 WHERE usage_day = (now() AT TIME ZONE 'UTC')::date`); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOnce(ctx); err != nil || !processed {
		t.Fatalf("next-day backlog did not resume: %v, %v", processed, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM moment_description_jobs`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("finished backlog = %d, %v", pending, err)
	}
	if inference.described != 2 || inference.embedded != 4 || inference.named != 0 {
		t.Fatalf("unexpected backlog inference: %+v", inference)
	}
	var title, description string
	if err := store.pool.QueryRow(ctx, `SELECT title, description FROM moments WHERE id = $1`, moment.ID).Scan(&title, &description); err != nil {
		t.Fatal(err)
	}
	if title != moment.Title || description != moment.Description {
		t.Fatal("backlog renamed a confirmed moment")
	}
}

func TestPostgresAlbumMetadataJobs(t *testing.T) {
	store, ownerID, candidates := momentTestStore(t, 3)
	ctx := context.Background()
	albumID := uuid.NewString()
	if _, err := store.pool.Exec(ctx, `INSERT INTO albums (id, owner_id, title) VALUES ($1, $2, 'Original album')`, albumID, ownerID); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if _, err := store.pool.Exec(ctx, `INSERT INTO album_media (album_id, upload_id) VALUES ($1, $2)`, albumID, candidate.ID); err != nil {
			t.Fatal(err)
		}
	}
	inference := &momentWorkflowInference{}
	service := moments.New(store, moments.WithQueuedGeneration(), moments.WithImageDescriber(inference), moments.WithMetadataSynthesizer(inference))
	request := func(method, owner, path string) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(func(context *gin.Context) {
			context.Set(auth.UserContextKey, auth.User{ID: owner})
			context.Next()
		})
		moments.RegisterRoutes(router, service)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		return response
	}
	path := "/albums/" + albumID + "/metadata-suggestion"
	response := request(http.MethodPost, ownerID, path)
	var job moments.AlbumMetadataJob
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil || response.Code != http.StatusAccepted || job.Status != "pending" {
		t.Fatalf("acceptance = %d %s, %v", response.Code, response.Body, err)
	}
	location := response.Header().Get("Location")
	if location != path+"/"+job.ID || inference.described != 0 || inference.named != 0 {
		t.Fatal("HTTP request ran inference or did not return a polling location")
	}
	var repeated moments.AlbumMetadataJob
	response = request(http.MethodPost, ownerID, path)
	if err := json.Unmarshal(response.Body.Bytes(), &repeated); err != nil || repeated.ID != job.ID {
		t.Fatalf("duplicate acceptance changed the active job: %s, %v", response.Body, err)
	}
	otherOwner := uuid.NewString()
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		target := path
		if method == http.MethodGet {
			target = location
		}
		if response := request(method, otherOwner, target); response.Code != http.StatusNotFound {
			t.Fatalf("cross-owner %s = %d %s", method, response.Code, response.Body)
		}
	}
	worker := moments.NewWorker(store, inference, inference, true, 1)
	for _, settings := range []struct {
		enabled bool
		limit   int
	}{{false, 1}, {true, 0}} {
		if processed, err := moments.NewWorker(store, inference, inference, settings.enabled, settings.limit).ProcessOnce(ctx); err != nil || processed {
			t.Fatalf("disabled worker processed metadata: %v, %v", processed, err)
		}
	}
	if processed, err := worker.ProcessOnce(ctx); err != nil || processed {
		t.Fatalf("uncached metadata should wait: %v, %v", processed, err)
	}
	for _, candidate := range candidates {
		if err := store.SaveImageDescriptions(ctx, []moments.ImageDescription{{MediaID: candidate.ID, Model: "vision", Description: "Cached description",
			People: []string{}, Activities: []string{}, Objects: []string{}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `UPDATE album_metadata_jobs SET scheduled_for = now() WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOnce(ctx); err != nil || !processed {
		t.Fatalf("metadata processing = %v, %v", processed, err)
	}
	response = request(http.MethodGet, ownerID, location)
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil || response.Code != http.StatusOK || job.Status != "completed" || job.Result == nil || job.Result.Title != "Original event" {
		t.Fatalf("completed poll = %d %s, %v", response.Code, response.Body, err)
	}
	if inference.described != 0 || inference.named != 1 {
		t.Fatalf("metadata bypassed the image quota: %+v", inference)
	}
	var title string
	if err := store.pool.QueryRow(ctx, `SELECT title FROM albums WHERE id = $1`, albumID).Scan(&title); err != nil || title != "Original album" {
		t.Fatalf("suggestion overwrote album metadata: %q, %v", title, err)
	}
	failed, err := store.EnqueueAlbumMetadataJob(ctx, albumID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := moments.NewWorker(store, &unexpectedMomentInference{}, &unexpectedMomentInference{}, true, 1).ProcessOnce(ctx); err == nil || !processed {
		t.Fatalf("provider failure was not recorded: %v, %v", processed, err)
	}
	failed, err = store.GetAlbumMetadataJob(ctx, failed.ID, albumID, ownerID)
	if err != nil || failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("failed job = %+v, %v", failed, err)
	}
	if _, err := store.EnqueueAlbumMetadataJob(ctx, albumID, ownerID); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimAlbumMetadataJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim = %v, %v", found, err)
	}
	if _, found, err := store.ClaimAlbumMetadataJob(ctx); err != nil || found {
		t.Fatalf("live lease was claimed twice: %v, %v", found, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE album_metadata_jobs SET lease_until = now() - INTERVAL '1 minute' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, found, err := store.ClaimAlbumMetadataJob(ctx)
	if err != nil || !found || reclaimed.LeaseToken == claimed.LeaseToken {
		t.Fatalf("expired lease was not reclaimed: %v, %v", found, err)
	}
	if err := store.FinishAlbumMetadataJob(ctx, claimed, job.Result, "", false); err == nil {
		t.Fatal("stale worker completed a reclaimed job")
	}
	if err := store.FinishAlbumMetadataJob(ctx, reclaimed, job.Result, "", false); err != nil {
		t.Fatal(err)
	}
}

type unexpectedMomentInference struct{ calls int }

func (inference *unexpectedMomentInference) Describe(context.Context, moments.Candidate) (moments.ImageDescription, error) {
	inference.calls++
	return moments.ImageDescription{}, fmt.Errorf("unexpected image inference")
}

func (inference *unexpectedMomentInference) Synthesize(context.Context, moments.Moment, []moments.ImageDescription) (moments.Metadata, error) {
	inference.calls++
	return moments.Metadata{}, fmt.Errorf("unexpected renaming")
}

func TestPostgresMomentWorkerLateAttachmentKeepsSummary(t *testing.T) {
	store, ownerID, candidates := momentTestStore(t, 6)
	ctx := context.Background()
	for _, candidate := range candidates {
		if err := store.SaveImageDescriptions(ctx, []moments.ImageDescription{{MediaID: candidate.ID, Model: "gemini", Description: "Family picnic by the lake",
			People: []string{}, Activities: []string{}, Objects: []string{}}}); err != nil {
			t.Fatal(err)
		}
	}
	moment := moments.Moment{ID: uuid.NewString(), OwnerID: ownerID, Title: "Original title", Description: "Original description",
		StartTime: candidates[0].CapturedAt, EndTime: candidates[4].CapturedAt, CoverMediaID: candidates[0].ID}
	if err := store.SaveEventGroup(ctx, moment, candidates[:5], true); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.EnqueueMomentCandidates(ctx, ownerID); err != nil || queued != 1 {
		t.Fatalf("late enqueue = %d, %v", queued, err)
	}
	inference := &unexpectedMomentInference{}
	if processed, err := moments.NewWorker(store, inference, inference, true, 100).ProcessOnce(ctx); err != nil || !processed {
		t.Fatalf("late processing = %v, %v", processed, err)
	}
	updated, err := store.GetMoment(ctx, moment.ID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != moment.Title || updated.Description != moment.Description || updated.ImageCount != 6 || inference.calls != 0 {
		t.Fatalf("late addition changed the summary or called inference: %+v, calls=%d", updated, inference.calls)
	}
}

func TestPostgresPreprocessingEnqueuesMomentDescription(t *testing.T) {
	store, _, candidates := momentTestStore(t, 2)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `UPDATE media_uploads SET moments_enabled = FALSE WHERE upload_id = $1`, candidates[1].ID); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if err := store.CompleteProcessingJob(ctx, processing.Job{ID: candidate.ID, UploadID: candidate.ID}, processing.Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	var queued int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM moment_description_jobs`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("preprocessing queued %d jobs: %v", queued, err)
	}
}

func TestPostgresMomentSemanticNeighborhood(t *testing.T) {
	store, ownerID, candidates := momentTestStore(t, 62)
	ctx := context.Background()
	for index, candidate := range candidates {
		description := moments.ImageDescription{MediaID: candidate.ID, Model: "vision", Description: "unrelated indoors",
			People: []string{}, Activities: []string{}, Objects: []string{}, Keywords: []string{"test"},
			EmbeddingModel: "text-model", Embedding: []float64{0, 1}}
		if index == 0 || index == len(candidates)-1 {
			description.Embedding = []float64{1, 0}
			if index == 0 {
				description.Description = "seaside swimming"
			} else {
				description.Description = "ocean holiday"
			}
		}
		if err := store.SaveImageDescriptions(ctx, []moments.ImageDescription{description}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `UPDATE media_exif SET captured_at = captured_at - interval '14 days' WHERE upload_id = $1`, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	job := moments.DescriptionJob{Candidate: candidates[len(candidates)-1], OwnerID: ownerID}
	photos, err := store.ListEventPhotos(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, photo := range photos {
		if photo.ID == candidates[0].ID {
			found = true
			if photo.Description.EmbeddingModel != "text-model" || len(photo.Description.Embedding) != 2 || len(photo.Description.Keywords) != 1 {
				t.Fatalf("cached descriptor features were lost: %+v", photo.Description)
			}
		}
	}
	if !found || len(photos) != 51 {
		t.Fatalf("semantic retrieval did not respect rank/cap: found=%v count=%d", found, len(photos))
	}
	if _, err := store.pool.Exec(ctx, `UPDATE media_image_descriptions SET text_embedding_model = 'incompatible' WHERE upload_id = $1`, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	photos, err = store.ListEventPhotos(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	for _, photo := range photos {
		if photo.ID == candidates[0].ID {
			t.Fatal("retrieved an incompatible vector without metadata support")
		}
	}
}

func TestPostgresMomentCosineRejectsInvalidVectors(t *testing.T) {
	store, _, _ := momentTestStore(t, 0)
	for _, vector := range [][]float64{nil, {}, {0, 0}, {1}, {math.NaN(), 1}, {math.Inf(1), 0}, {1e308, 0}} {
		var score *float64
		if err := store.pool.QueryRow(context.Background(), `SELECT gallery_text_cosine($1, $2)`, []float64{1, 0}, vector).Scan(&score); err != nil {
			t.Fatal(err)
		}
		if score != nil {
			t.Fatalf("invalid vector produced score %f", *score)
		}
	}
	var score float64
	if err := store.pool.QueryRow(context.Background(), `SELECT gallery_text_cosine($1, $2)`, []float64{1, 0}, []float64{0.9, 0.1}).Scan(&score); err != nil || math.Abs(score-0.9938837346736189) > 1e-12 {
		t.Fatalf("cosine = %f, %v", score, err)
	}
}

func TestPostgresMomentClaimsSerializeEachOwner(t *testing.T) {
	store, ownerID, _ := momentTestStore(t, 2)
	ctx := context.Background()
	if _, err := store.EnqueueMomentCandidates(ctx, ownerID); err != nil {
		t.Fatal(err)
	}
	first, found, err := store.ClaimMomentJob(ctx, 100)
	if err != nil || !found {
		t.Fatalf("first claim = %v, %v", found, err)
	}
	if _, found, err := store.ClaimMomentJob(ctx, 100); err != nil || found {
		t.Fatalf("overlapping owner claim = %v, %v", found, err)
	}
	otherOwner := uuid.NewString()
	if _, err := store.pool.Exec(ctx, `INSERT INTO users (id, username, password_hash) VALUES ($1, 'other', 'test')`, otherOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO media_uploads (upload_id, owner_id, filename, mime_type, size, sha256, media_path, created_at)
		VALUES ('other-photo', $1, 'other.jpg', 'image/jpeg', 1, 'other', 'other.jpg', now())`, otherOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO moment_description_jobs (upload_id) VALUES ('other-photo')`); err != nil {
		t.Fatal(err)
	}
	other, found, err := store.ClaimMomentJob(ctx, 100)
	if err != nil || !found || other.OwnerID != otherOwner {
		t.Fatalf("different owner blocked: %+v, %v", other, err)
	}
	if err := store.CompleteMomentJob(ctx, first); err != nil {
		t.Fatal(err)
	}
	next, found, err := store.ClaimMomentJob(ctx, 100)
	if err != nil || !found || next.OwnerID != ownerID || next.ID == first.ID {
		t.Fatalf("owner did not resume: %+v, %v", next, err)
	}
}

type momentWorkflowInference struct {
	described, embedded, named int
}

func (inference *momentWorkflowInference) Describe(_ context.Context, candidate moments.Candidate) (moments.ImageDescription, error) {
	inference.described++
	return moments.ImageDescription{Model: "vision", Description: candidate.ID,
		People: []string{}, Activities: []string{}, Objects: []string{}}, nil
}

func (inference *momentWorkflowInference) TextEmbeddingModel() string { return "descriptor-text:2" }

func (inference *momentWorkflowInference) EmbedDescription(context.Context, moments.ImageDescription) ([]float64, error) {
	inference.embedded++
	return []float64{1, 0}, nil
}

func (inference *momentWorkflowInference) Synthesize(context.Context, moments.Moment, []moments.ImageDescription) (moments.Metadata, error) {
	inference.named++
	return moments.Metadata{Title: "Original event", Description: "Original description"}, nil
}

func TestPostgresMomentUploadToEventFlow(t *testing.T) {
	store, ownerID, candidates := momentTestStore(t, 6)
	ctx := context.Background()
	inference := &momentWorkflowInference{}
	worker := moments.NewWorker(store, inference, inference, true, 100)
	var momentID string
	for index, candidate := range candidates {
		if err := store.CompleteProcessingJob(ctx, processing.Job{ID: candidate.ID, UploadID: candidate.ID, OwnerID: ownerID}, processing.Metadata{}); err != nil {
			t.Fatal(err)
		}
		if inference.described != index || inference.embedded != index {
			t.Fatal("preprocessing invoked inference")
		}
		if processed, err := moments.NewWorker(store, inference, inference, false, 100).ProcessOnce(ctx); err != nil || processed {
			t.Fatalf("disabled worker = %v, %v", processed, err)
		}
		if processed, err := worker.ProcessOnce(ctx); err != nil || !processed {
			t.Fatalf("photo %d = %v, %v", index, processed, err)
		}
		var momentCount, jobCount, cachedCount int
		if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM moments`).Scan(&momentCount); err != nil {
			t.Fatal(err)
		}
		if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM moment_description_jobs`).Scan(&jobCount); err != nil {
			t.Fatal(err)
		}
		if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_image_descriptions WHERE text_embedding_model = $1 AND cardinality(text_embedding) = 2`, inference.TextEmbeddingModel()).Scan(&cachedCount); err != nil {
			t.Fatal(err)
		}
		if jobCount != 0 || cachedCount != index+1 || inference.described != index+1 || inference.embedded != index+1 {
			t.Fatal("job completion lost or repeated descriptor features")
		}
		if index < 4 {
			if momentCount != 0 || inference.named != 0 {
				t.Fatal("fewer than five photos created a moment")
			}
			continue
		}
		if momentCount != 1 || inference.named != 1 {
			t.Fatal("event was recreated or renamed")
		}
		var currentID, title, description string
		var imageCount int
		if err := store.pool.QueryRow(ctx, `SELECT id::text, title, description, image_count FROM moments`).Scan(&currentID, &title, &description, &imageCount); err != nil {
			t.Fatal(err)
		}
		if index == 4 {
			momentID = currentID
		}
		if currentID != momentID || title != "Original event" || description != "Original description" || imageCount != index+1 {
			t.Fatal("late addition changed the confirmed moment")
		}
	}
}
