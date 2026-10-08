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
	quota       *int64
}

func (repository *recordingMediaRepository) SaveCompletedMedia(_ context.Context, media CompletedMedia) error {
	if repository.saveErr != nil {
		return repository.saveErr
	}
	repository.media = append(repository.media, media)
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

	firstService := newTestService(t, tmpDir, mediaDir, int64(len(content)))
	firstRouter := newTestRouter(t, firstService)
	created := createUpload(t, firstRouter, fmt.Sprintf(
		`{"filename":"../video.mp4","size":%d,"sha256":"%s"}`,
		len(content), hex.EncodeToString(digest[:]),
	))

	if _, err := os.Stat(filepath.Join(tmpDir, created.ID, metadataFilename)); err != nil {
		t.Fatalf("metadata was not persisted: %v", err)
	}

	recoveredService := newTestService(t, tmpDir, mediaDir, int64(len(content)))
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
	}{
		{name: "active sessions", options: []Option{WithMaxActiveUploads(1)}},
		{name: "pending bytes", options: []Option{WithMaxPendingUploadSize(6)}},
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
		})
	}
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
	repository := &recordingMediaRepository{}
	service := newTestService(
		t,
		filepath.Join(root, "tmp"),
		filepath.Join(root, "media"),
		int64(len(content)),
		WithMediaRepository(repository),
	)
	router := newTestRouter(t, service)
	created := createUpload(t, router, fmt.Sprintf(`{"filename":"video.mp4","size":%d}`, len(content)))

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
	service := newTestService(t, filepath.Join(root, "tmp"), filepath.Join(root, "media"), 4)
	router := newTestRouter(t, service)
	created := createUpload(t, router, `{"filename":"old.mp4","size":4}`)
	removed, err := service.CleanupExpiredUploads(time.Now().Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d uploads, want 1", removed)
	}
	response := performRequest(router, http.MethodGet, "/media/upload/"+created.ID, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("got %d, want %d", response.Code, http.StatusNotFound)
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
