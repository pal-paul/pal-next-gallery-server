package uploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pal-next-gallery-server/app/auth"
	"pal-next-gallery-server/app/batch"

	"github.com/gin-gonic/gin"
)

type createResponse struct {
	ID string `json:"id"`
}

type completeResponse struct {
	MediaPath string `json:"media_path"`
}

type recordingMediaRepository struct {
	media       []CompletedMedia
	duplicate   *CompletedMedia
	storedBytes int64
	saveErr     error
	releaseErr  error
	quota       *int64
	pending     map[string]Upload
}

func (repository *recordingMediaRepository) SaveCompletedMedia(_ context.Context, media CompletedMedia) error {
	if repository.saveErr != nil {
		return repository.saveErr
	}
	repository.media = append(repository.media, media)
	repository.storedBytes += media.Size
	delete(repository.pending, media.UploadID)
	return nil
}

func (repository *recordingMediaRepository) ReserveUploadStorage(ctx context.Context, upload Upload, limit int64) error {
	if repository.quota != nil {
		limit = *repository.quota
	}
	used := repository.storedBytes
	for _, pending := range repository.pending {
		if pending.OwnerID == upload.OwnerID && pending.ID != upload.ID {
			used += pending.Size
		}
	}
	if quotaExceeded(used, upload.Size, limit) {
		return ErrStorageQuota
	}
	return repository.RestoreUploadStorage(ctx, upload)
}

func (repository *recordingMediaRepository) RestoreUploadStorage(_ context.Context, upload Upload) error {
	if repository.pending == nil {
		repository.pending = make(map[string]Upload)
	}
	repository.pending[upload.ID] = upload
	return nil
}

func (repository *recordingMediaRepository) ReleaseUploadStorage(_ context.Context, ownerID, uploadID string) error {
	if repository.releaseErr != nil {
		return repository.releaseErr
	}
	if repository.pending[uploadID].OwnerID == ownerID {
		delete(repository.pending, uploadID)
	}
	return nil
}

func (repository *recordingMediaRepository) OwnerStorageBytes(context.Context, string) (int64, error) {
	return repository.storedBytes, nil
}

func (repository *recordingMediaRepository) OwnerStorageQuota(context.Context, string) (int64, bool, error) {
	if repository.quota == nil {
		return 0, false, nil
	}
	return *repository.quota, true, nil
}

func (repository *recordingMediaRepository) FindOwnedMediaBySHA256(_ context.Context, ownerID, checksum string) (CompletedMedia, bool, error) {
	if repository.duplicate == nil || repository.duplicate.OwnerID != ownerID || repository.duplicate.SHA256 != checksum {
		return CompletedMedia{}, false, nil
	}
	return *repository.duplicate, true, nil
}
func (repository *recordingMediaRepository) ValidateUploadBatch(context.Context, string, string) error {
	return nil
}

type diskWriteFunc func([]byte) (int, error)

func (write diskWriteFunc) Write(data []byte) (int, error) {
	return write(data)
}

func TestCopyWithDiskReserveCoordinatesConcurrentWriters(t *testing.T) {
	var mutex sync.Mutex
	available := int64(100)
	destination := diskWriteFunc(func(data []byte) (int, error) {
		mutex.Lock()
		defer mutex.Unlock()
		available -= int64(len(data))
		return len(data), nil
	})
	check := func(string) (int64, error) {
		mutex.Lock()
		defer mutex.Unlock()
		return available, nil
	}
	start := make(chan struct{})
	results := make(chan error, 32)
	for index := 0; index < 32; index++ {
		go func() {
			<-start
			_, err := CopyWithDiskReserve(context.Background(), destination, strings.NewReader(strings.Repeat("x", 30)), "media", 60, check)
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for index := 0; index < 32; index++ {
		if err := <-results; err == nil {
			succeeded++
		} else if !errors.Is(err, ErrInsufficientDiskSpace) {
			t.Errorf("unexpected write error: %v", err)
		}
	}
	if succeeded != 1 || available != 70 {
		t.Fatalf("concurrent writers exceeded capacity: succeeded=%d, available=%d", succeeded, available)
	}
}

func TestCopyWithDiskReserveBoundsWritesAndRechecksCapacity(t *testing.T) {
	available := int64(200000)
	writes := 0
	destination := diskWriteFunc(func(data []byte) (int, error) {
		if len(data) > 32*1024 {
			t.Fatalf("unbounded write: %d bytes", len(data))
		}
		writes++
		available = 5
		return len(data), nil
	})
	written, err := CopyWithDiskReserve(context.Background(), destination, strings.NewReader(strings.Repeat("x", 100000)),
		"media", 5, func(string) (int64, error) { return available, nil })
	if !errors.Is(err, ErrInsufficientDiskSpace) || written != 32*1024 || writes != 1 {
		t.Fatalf("did not stop at reserve: written=%d, writes=%d, error=%v", written, writes, err)
	}
}

func TestCopyWithDiskReserveHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var destination bytes.Buffer
	written, err := CopyWithDiskReserve(ctx, &destination, strings.NewReader("photo"), "media", 0,
		func(string) (int64, error) { return 100, nil })
	if !errors.Is(err, context.Canceled) || written != 0 || destination.Len() != 0 {
		t.Fatalf("canceled extraction wrote data: %d, %v", written, err)
	}
}

func TestCheckDuplicateAndCreateConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checksum := strings.Repeat("a", 64)
	existing := CompletedMedia{
		UploadID: "existing-upload", OwnerID: "11111111-1111-1111-1111-111111111111",
		Filename: "existing.mp4", Size: 42, SHA256: checksum, CreatedAt: time.Now(),
	}
	root := t.TempDir()
	repository := &recordingMediaRepository{duplicate: &existing}
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 100, WithMediaRepository(repository))
	router := newTestRouter(t, service)

	response := performRequest(router, http.MethodPost, "/media/upload/check", bytes.NewBufferString(`{"sha256":"`+checksum+`"}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate check returned %d: %s", response.Code, response.Body.String())
	}
	response = performRequest(router, http.MethodPost, "/media/upload", bytes.NewBufferString(`{"filename":"copy.mp4","size":42,"sha256":"`+checksum+`"}`))
	if response.Code != http.StatusConflict {
		t.Fatalf("create returned %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
}

func TestUploadSurvivesRestartAndCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	mediaDir := filepath.Join(root, "media")
	content := []byte("durable media")
	digest := sha256.Sum256(content)

	repository := &recordingMediaRepository{}
	firstService := newTestService(t, tmpDir, mediaDir, int64(len(content)), WithMediaRepository(repository))
	firstRouter := newTestRouter(t, firstService)
	created := createUpload(t, firstRouter, fmt.Sprintf(
		`{"filename":"../video.mp4","size":%d,"sha256":"%s"}`,
		len(content), hex.EncodeToString(digest[:]),
	))

	if _, err := os.Stat(filepath.Join(tmpDir, created.ID, metadataFilename)); err != nil {
		t.Fatalf("metadata was not persisted: %v", err)
	}

	repository.pending = nil
	recoveredService := newTestService(t, tmpDir, mediaDir, int64(len(content)), WithMediaRepository(repository))
	if repository.pending[created.ID].Size != int64(len(content)) {
		t.Fatal("recovered upload did not restore its storage reservation")
	}
	recoveredRouter := newTestRouter(t, recoveredService)
	response := performRequest(recoveredRouter, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewReader(content))
	if response.Code != http.StatusOK {
		t.Fatalf("upload part returned %d: %s", response.Code, response.Body.String())
	}

	response = performRequest(recoveredRouter, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("complete returned %d: %s", response.Code, response.Body.String())
	}
	var completed completeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &completed); err != nil {
		t.Fatalf("decode completion response: %v", err)
	}
	if completed.MediaPath == "" || filepath.IsAbs(completed.MediaPath) {
		t.Fatalf("expected a relative media path, got %q", completed.MediaPath)
	}
	stored, err := os.ReadFile(filepath.Join(mediaDir, filepath.FromSlash(completed.MediaPath)))
	if err != nil {
		t.Fatalf("read completed media: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("completed content mismatch: got %q", stored)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, created.ID)); !os.IsNotExist(err) {
		t.Fatalf("temporary upload directory still exists: %v", err)
	}
	if len(repository.pending) != 0 {
		t.Fatal("completed upload retained its storage reservation")
	}
}

func TestCreateUploadValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4)
	router := newTestRouter(t, service)

	tests := []struct {
		name string
		body string
	}{
		{name: "too large", body: `{"filename":"video.mp4","size":5}`},
		{name: "invalid filename", body: `{"filename":"..","size":4}`},
		{name: "invalid checksum", body: `{"filename":"video.mp4","size":4,"sha256":"invalid"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(router, http.MethodPost, "/media/upload", bytes.NewBufferString(test.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCreateUploadEnforcesPerUserQuotas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		options []Option
		code    string
	}{
		{name: "active sessions", options: []Option{WithMaxActiveUploads(1)}, code: "upload_active_limit"},
		{name: "pending bytes", options: []Option{WithMaxPendingUploadSize(6)}, code: "upload_pending_limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4, test.options...)
			router := newTestRouter(t, service)
			createUpload(t, router, `{"filename":"first.mp4","size":4}`)
			response := performRequest(router, http.MethodPost, "/media/upload", bytes.NewBufferString(`{"filename":"second.mp4","size":4}`))
			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("got %d, want %d: %s", response.Code, http.StatusTooManyRequests, response.Body.String())
			}
			var payload struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Code != test.code {
				t.Fatalf("unexpected quota error: %s (%v)", response.Body.String(), err)
			}
			if response.Header().Get("Retry-After") != "5" {
				t.Fatalf("temporary quota response has no retry delay: %v", response.Header())
			}
		})
	}
}

type recordingBatchRepository struct {
	batch batch.Batch
}

func (repository *recordingBatchRepository) CreateUploadBatch(_ context.Context, item batch.Batch) error {
	repository.batch = item
	return nil
}

func (repository *recordingBatchRepository) GetUploadBatch(_ context.Context, id, ownerID string) (batch.Batch, error) {
	if repository.batch.ID != id || repository.batch.OwnerID != ownerID {
		return batch.Batch{}, errors.New("upload batch not found")
	}
	return repository.batch, nil
}

func (repository *recordingBatchRepository) CancelUploadBatch(ctx context.Context, id, ownerID string) error {
	if _, err := repository.GetUploadBatch(ctx, id, ownerID); err != nil {
		return err
	}
	repository.batch.Status = "canceled"
	return nil
}

func TestCancelBatchUploadsReleasesQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	repository := &recordingMediaRepository{}
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4,
		WithMediaRepository(repository), WithMaxActiveUploads(1))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"first.mp4","size":4,"batchId":"batch-1"}`)
	response := performRequest(router, http.MethodPost, "/media/upload", bytes.NewBufferString(`{"filename":"second.mp4","size":4}`))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upload limit, got %d: %s", response.Code, response.Body.String())
	}
	ownerID := repository.pending[created.ID].OwnerID
	if err := service.CancelBatchUploads(context.Background(), "batch-1", "another-owner"); err != nil {
		t.Fatal(err)
	}
	if len(repository.pending) != 1 {
		t.Fatal("another owner canceled the upload")
	}
	if err := service.CancelBatchUploads(context.Background(), "another-batch", ownerID); err != nil {
		t.Fatal(err)
	}
	if len(repository.pending) != 1 {
		t.Fatal("canceling another batch removed the upload")
	}
	batchRepository := &recordingBatchRepository{batch: batch.Batch{ID: "batch-1", OwnerID: ownerID, Status: "active"}}
	batchService := batch.New(batchRepository, service)
	router.POST("/media/upload-batches/:id/cancel", batchService.Cancel)
	request := httptest.NewRequest(http.MethodPost, "/media/upload-batches/batch-1/cancel", nil)
	request.Header.Set("X-Test-User", "another-owner")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || len(repository.pending) != 1 {
		t.Fatalf("another owner canceled the batch: %d", response.Code)
	}
	repository.releaseErr = errors.New("storage release failed")
	response = performRequest(router, http.MethodPost, "/media/upload-batches/batch-1/cancel", nil)
	if response.Code != http.StatusInternalServerError || len(repository.pending) != 1 {
		t.Fatalf("failed cleanup must retain a retryable session: %d", response.Code)
	}
	repository.releaseErr = nil
	for attempt := 0; attempt < 2; attempt++ {
		response = performRequest(router, http.MethodPost, "/media/upload-batches/batch-1/cancel", nil)
		if response.Code != http.StatusNoContent {
			t.Fatalf("cancel returned %d: %s", response.Code, response.Body.String())
		}
	}
	if len(repository.pending) != 0 {
		t.Fatal("canceled upload retained its storage reservation")
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", created.ID)); !os.IsNotExist(err) {
		t.Fatalf("canceled upload directory still exists: %v", err)
	}
	createUpload(t, router, `{"filename":"second.mp4","size":4}`)
}

func TestCreateUploadEnforcesTotalStorageQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	repository := &recordingMediaRepository{storedBytes: 3}
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4,
		WithMediaRepository(repository), WithMaxUserStorageSize(6))
	router := newTestRouter(t, service)
	response := performRequest(router, http.MethodPost, "/media/upload", bytes.NewBufferString(`{"filename":"video.mp4","size":4}`))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want %d: %s", response.Code, http.StatusTooManyRequests, response.Body.String())
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Code != "upload_storage_quota_exceeded" {
		t.Fatalf("unexpected storage quota error: %s (%v)", response.Body.String(), err)
	}
	if response.Header().Get("Retry-After") != "" {
		t.Fatalf("storage quota must not request automatic retries: %v", response.Header())
	}
	entries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("quota rejection retained upload metadata: %v, %v", entries, err)
	}
}

func TestUploadPartRejectsLowDiskSpace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4,
		WithMinDiskFree(10), withDiskFree(func(string) (int64, error) { return 10, nil }))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"video.mp4","size":4}`)
	response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewBufferString("data"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want %d: %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
}

func TestUploadStopsWhenDiskSpaceDropsDuringWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			root := t.TempDir()
			service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4, WithMinDiskFree(10)).(*service)
			router := newTestRouter(t, service)
			created := createUpload(t, router, `{"filename":"video.mp4","size":4}`)
			if complete {
				response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewBufferString("data"))
				if response.Code != http.StatusOK {
					t.Fatalf("upload part: %d: %s", response.Code, response.Body.String())
				}
			}
			checks := 0
			service.diskFree = func(string) (int64, error) {
				checks++
				if checks == 1 {
					return 100, nil
				}
				return 10, nil
			}
			var response *httptest.ResponseRecorder
			if complete {
				response = performRequest(router, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
			} else {
				response = performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewBufferString("data"))
			}
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
			}
			if complete {
				if _, err := os.Stat(filepath.Join(service.tmpDir, created.ID, "00000000.part")); err != nil {
					t.Fatalf("resumable chunk was removed: %v", err)
				}
			}
			if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err == nil && entry.Type().IsRegular() && strings.HasSuffix(path, ".tmp") {
					t.Errorf("incomplete write remains: %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentChunkReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4)
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"video.mp4","size":4}`)

	contents := [][]byte{[]byte("aaaa"), []byte("bbbb")}
	var waitGroup sync.WaitGroup
	errors := make(chan string, len(contents))
	for _, content := range contents {
		content := content
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewReader(content))
			if response.Code != http.StatusOK {
				errors <- fmt.Sprintf("status %d: %s", response.Code, response.Body.String())
			}
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	response := performRequest(router, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("complete returned %d: %s", response.Code, response.Body.String())
	}
}

func TestCompleteUploadPersistsMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	content := []byte("persisted media")
	capturedAt := time.Date(2022, time.March, 14, 15, 9, 26, 0, time.UTC)
	repository := &recordingMediaRepository{}
	service := newTestService(
		t,
		filepath.Join(root, "tmp"),
		filepath.Join(root, "media"),
		int64(len(content)),
		WithMediaRepository(repository),
	)
	router := newTestRouter(t, service)
	created := createUpload(t, router, fmt.Sprintf(
		`{"filename":"video.mp4","size":%d,"capturedAt":%q}`,
		len(content), capturedAt.Format(time.RFC3339),
	))

	response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewReader(content))
	if response.Code != http.StatusOK {
		t.Fatalf("upload part returned %d: %s", response.Code, response.Body.String())
	}
	response = performRequest(router, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("complete returned %d: %s", response.Code, response.Body.String())
	}
	if len(repository.media) != 1 {
		t.Fatalf("got %d media records, want 1", len(repository.media))
	}
	media := repository.media[0]
	if media.UploadID != created.ID || media.MediaPath == "" || media.SHA256 == "" {
		t.Fatalf("incomplete media record: %#v", media)
	}
	if media.CreatedAt.IsZero() {
		t.Fatal("expected media completion time to be persisted")
	}
	if media.CapturedAt == nil || !media.CapturedAt.Equal(capturedAt) {
		t.Fatalf("captured time = %v, want %v", media.CapturedAt, capturedAt)
	}
}

func TestCompleteUploadRemovesFileWhenPersistenceFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	mediaDir := filepath.Join(root, "media")
	repository := &recordingMediaRepository{saveErr: errors.New("database unavailable")}
	service := newTestService(t, tmpDir, mediaDir, 4, WithMediaRepository(repository), WithMinDiskFree(0))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"video.mp4","size":4}`)
	if response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewBufferString("data")); response.Code != http.StatusOK {
		t.Fatalf("upload part returned %d: %s", response.Code, response.Body.String())
	}
	response := performRequest(router, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d: %s", response.Code, http.StatusInternalServerError, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(tmpDir, created.ID, "00000000.part")); err != nil {
		t.Fatalf("resumable chunk was not retained: %v", err)
	}
	err := filepath.WalkDir(mediaDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && entry.Type().IsRegular() {
			t.Errorf("uncommitted media file remains at %s", path)
		}
		return walkErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompleteDuplicateReleasesPendingUploadQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	repository := &recordingMediaRepository{saveErr: ErrDuplicateMedia}
	service := newTestService(t, tmpDir, filepath.Join(root, "media"), 4,
		WithMediaRepository(repository), WithMaxActiveUploads(1), WithMinDiskFree(0))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"duplicate.mp4","size":4}`)
	if response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewBufferString("data")); response.Code != http.StatusOK {
		t.Fatalf("upload part returned %d: %s", response.Code, response.Body.String())
	}

	response := performRequest(router, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("got %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(tmpDir, created.ID)); !os.IsNotExist(err) {
		t.Fatalf("duplicate upload directory still exists: %v", err)
	}
	if len(repository.pending) != 0 {
		t.Fatal("duplicate retained its storage reservation")
	}
	response = performRequest(router, http.MethodGet, "/media/upload/"+created.ID, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("duplicate upload status returned %d, want %d", response.Code, http.StatusNotFound)
	}
	createUpload(t, router, `{"filename":"next.mp4","size":4}`)
}

func TestUploadSessionIsPrivateToOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4)
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"private.mp4","size":4}`)

	request := httptest.NewRequest(http.MethodGet, "/media/upload/"+created.ID, nil)
	request.Header.Set("X-Test-User", "22222222-2222-2222-2222-222222222222")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("other user got status %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestCleanupExpiredUploadsRemovesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	repository := &recordingMediaRepository{}
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4, WithMediaRepository(repository))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"old.mp4","size":4}`)
	removed, err := service.CleanupExpiredUploads(time.Now().Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d uploads, want 1", removed)
	}
	if len(repository.pending) != 0 {
		t.Fatal("expired upload retained its storage reservation")
	}
	response := performRequest(router, http.MethodGet, "/media/upload/"+created.ID, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("got %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestDeleteUploadReleasesStorageReservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	repository := &recordingMediaRepository{}
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4,
		WithMediaRepository(repository), WithMaxUserStorageSize(4))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"cancelled.mp4","size":4}`)
	response := performRequest(router, http.MethodDelete, "/media/upload/"+created.ID, nil)
	if response.Code != http.StatusOK || len(repository.pending) != 0 {
		t.Fatalf("cancellation did not release storage: %d, %v", response.Code, repository.pending)
	}
	createUpload(t, router, `{"filename":"next.mp4","size":4}`)
}

func TestCompleteUploadReportsStorageQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	repository := &recordingMediaRepository{saveErr: ErrStorageQuota}
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4,
		WithMediaRepository(repository), WithMinDiskFree(0))
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"video.mp4","size":4}`)
	if response := performRequest(router, http.MethodPut, "/media/upload/"+created.ID+"/parts/0", bytes.NewBufferString("data")); response.Code != http.StatusOK {
		t.Fatalf("upload part returned %d: %s", response.Code, response.Body.String())
	}
	response := performRequest(router, http.MethodPost, "/media/upload/"+created.ID+"/complete", nil)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), `"code":"upload_storage_quota_exceeded"`) {
		t.Fatalf("unexpected storage quota response: %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "" {
		t.Fatal("storage quota must not request automatic retries")
	}
	if repository.pending[created.ID].Size != 4 {
		t.Fatal("failed completion did not retain its reservation")
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", created.ID, "00000000.part")); err != nil {
		t.Fatalf("failed completion did not retain resumable chunks: %v", err)
	}
}

func newTestService(t *testing.T, tmpDir string, mediaDir string, maxUploadSize int64, options ...Option) IUploaderService {
	t.Helper()
	options = append([]Option{WithMaxUploadSize(maxUploadSize)}, options...)
	service, err := New(tmpDir, mediaDir, options...)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return service
}

func newTestRouter(t *testing.T, service IUploaderService) *gin.Engine {
	t.Helper()
	engine := gin.New()
	engine.Use(func(context *gin.Context) {
		userID := context.GetHeader("X-Test-User")
		if userID == "" {
			userID = "11111111-1111-1111-1111-111111111111"
		}
		context.Set(auth.UserContextKey, auth.User{ID: userID, Username: "test", Role: "user", UploadFolder: "test"})
		context.Next()
	})
	router, err := SetupRouter(engine, service)
	if err != nil {
		t.Fatalf("setup router: %v", err)
	}
	return router
}

func createUpload(t *testing.T, router http.Handler, body string) createResponse {
	t.Helper()
	response := performRequest(router, http.MethodPost, "/media/upload", bytes.NewBufferString(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}
	var created createResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created
}

func performRequest(handler http.Handler, method string, target string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	if method == http.MethodPost && body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
