package takeoutimport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pal-next-gallery-server/app/auth"
	"pal-next-gallery-server/app/uploader"

	"github.com/gin-gonic/gin"
)

type recordingRepository struct {
	media   map[string]uploader.CompletedMedia
	saved   []uploader.CompletedMedia
	albums  map[string][]string
	saveErr error
}

func newRecordingRepository() *recordingRepository {
	return &recordingRepository{media: make(map[string]uploader.CompletedMedia), albums: make(map[string][]string)}
}

func (repository *recordingRepository) SaveCompletedMedia(_ context.Context, media uploader.CompletedMedia) error {
	if _, duplicate := repository.media[media.SHA256]; duplicate {
		return uploader.ErrDuplicateMedia
	}
	if repository.saveErr != nil {
		return repository.saveErr
	}
	var used int64
	for _, stored := range repository.media {
		used += stored.Size
	}
	if media.StorageQuota > 0 && (used > media.StorageQuota || media.Size > media.StorageQuota-used) {
		return uploader.ErrStorageQuota
	}
	repository.media[media.SHA256] = media
	repository.saved = append(repository.saved, media)
	return nil
}

func (repository *recordingRepository) OwnerStorageBytes(context.Context, string) (int64, error) {
	var total int64
	for _, media := range repository.media {
		total += media.Size
	}
	return total, nil
}

func (repository *recordingRepository) OwnerStorageQuota(context.Context, string) (int64, bool, error) {
	return 0, false, nil
}

func (repository *recordingRepository) FindOwnedMediaBySHA256(_ context.Context, _, checksum string) (uploader.CompletedMedia, bool, error) {
	media, found := repository.media[checksum]
	return media, found, nil
}

func (repository *recordingRepository) ValidateUploadBatch(context.Context, string, string) error {
	return nil
}

func (repository *recordingRepository) UpsertTakeoutAlbum(_ context.Context, _, sourceKey, _ string, mediaIDs []string) error {
	repository.albums[sourceKey] = append(repository.albums[sourceKey], mediaIDs...)
	return nil
}

func TestImportArchiveDisablesMomentsAndRecreatesAlbums(t *testing.T) {
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	archive := testArchive(t, map[string][]byte{
		"Takeout/Google Photos/City Break/photo.jpg": []byte("first-photo"),
		"Takeout/Google Photos/City Break/photo.jpg.json": mustJSON(t, map[string]any{
			"title": "photo.jpg", "photoTakenTime": map[string]string{"timestamp": "1609459200"},
		}),
		"Takeout/Google Photos/Photos from 2024/other.jpg": []byte("second-photo"),
	})
	user := auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"}

	result, err := service.importArchive(context.Background(), archive, user, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.Albums != 1 || result.MomentsEnabled {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(repository.saved) != 2 {
		t.Fatalf("saved %d media, want 2", len(repository.saved))
	}
	for _, media := range repository.saved {
		if media.MomentsEnabled == nil || *media.MomentsEnabled {
			t.Fatalf("moments were not disabled for %q", media.Filename)
		}
	}
	if repository.saved[0].CapturedAt == nil || repository.saved[0].CapturedAt.Unix() != 1609459200 {
		t.Fatal("Takeout capture time was not preserved")
	}
	if len(repository.albums["city break"]) != 1 {
		t.Fatalf("unexpected album members: %#v", repository.albums)
	}

	result, err = service.importArchive(context.Background(), archive, user, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 0 || result.Duplicates != 2 {
		t.Fatalf("retry was not deduplicated: %+v", result)
	}
}

func TestImportArchiveCanEnableMoments(t *testing.T) {
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	archive := testArchive(t, map[string][]byte{
		"Takeout/Google Photos/Photos from 2025/photo.jpg": []byte("photo"),
	})
	_, err := service.importArchive(context.Background(), archive,
		auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.saved) != 1 || repository.saved[0].MomentsEnabled == nil || !*repository.saved[0].MomentsEnabled {
		t.Fatal("explicit Moments opt-in was not persisted")
	}
	if repository.saved[0].StorageQuota != service.maxStorageSize {
		t.Fatal("import did not pass the default quota to atomic persistence")
	}
}

func TestImportHandlerDefaultsMomentsToEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	response := performImportRequest(t, service, nil)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(repository.saved) != 1 || repository.saved[0].MomentsEnabled == nil || !*repository.saved[0].MomentsEnabled {
		t.Fatal("handler did not default automatic Moments to enabled")
	}
}

func TestImportHandlerCanDisableAutomaticMoments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	disabled := false
	response := performImportRequest(t, service, &disabled)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(repository.saved) != 1 || repository.saved[0].MomentsEnabled == nil || *repository.saved[0].MomentsEnabled {
		t.Fatal("handler did not disable automatic Moments")
	}
}

func TestImportHandlerReportsStorageQuotaAndRemovesFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		limit   int64
		saveErr error
	}{
		{name: "default quota", limit: 4},
		{name: "concurrent reservation", limit: 100, saveErr: uploader.ErrStorageQuota},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newRecordingRepository()
			repository.saveErr = test.saveErr
			service := newTestService(t, repository)
			service.maxStorageSize = test.limit
			response := performImportRequest(t, service, nil)
			if response.Code != http.StatusRequestEntityTooLarge || len(repository.saved) != 0 {
				t.Fatalf("quota rejection: status = %d, body = %s", response.Code, response.Body.String())
			}
			if err := filepath.WalkDir(service.mediaDir, func(path string, entry os.DirEntry, err error) error {
				if err == nil && entry.Type().IsRegular() {
					t.Errorf("uncommitted import file remains: %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImportHandlerChecksDiskCapacityBeforeExtraction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name      string
		available int64
		checkErr  error
		status    int
	}{
		{name: "reserve would be consumed", available: 9, status: http.StatusServiceUnavailable},
		{name: "already below reserve", available: 4, status: http.StatusServiceUnavailable},
		{name: "stat failure", checkErr: errors.New("stat failed"), status: http.StatusInternalServerError},
		{name: "exact capacity", available: 10, status: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newRecordingRepository()
			service := newTestService(t, repository)
			service.minDiskFree = 5
			service.diskFree = func(directory string) (int64, error) {
				if directory == service.tmpDir {
					return 1 << 20, nil
				}
				if directory != service.mediaDir {
					t.Errorf("capacity checked on %q, want media volume %q", directory, service.mediaDir)
				}
				return test.available, test.checkErr
			}
			response := performImportRequest(t, service, nil)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.status != http.StatusCreated {
				entries, err := os.ReadDir(service.mediaDir)
				if err != nil || len(entries) != 0 || len(repository.saved) != 0 {
					t.Fatalf("disk rejection created media: entries=%v, saved=%d, error=%v", entries, len(repository.saved), err)
				}
			}
		})
	}
}

func TestImportCleansUpWhenDiskSpaceDropsDuringExtraction(t *testing.T) {
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	service.minDiskFree = 5
	checks := 0
	service.diskFree = func(string) (int64, error) {
		checks++
		if checks <= 2 {
			return 200000, nil
		}
		return 5, nil
	}
	archive := testArchive(t, map[string][]byte{
		"Takeout/Google Photos/Photos from 2025/photo.jpg": []byte(strings.Repeat("x", 100000)),
	})
	_, err := service.importArchive(context.Background(), archive, auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"}, true)
	if !errors.Is(err, ErrInsufficientDiskSpace) || len(repository.saved) != 0 {
		t.Fatalf("did not reject extraction at disk reserve: %v", err)
	}
	if err := filepath.WalkDir(service.mediaDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			t.Errorf("partial extracted file remains: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func performImportRequest(t *testing.T, service *Service, momentsEnabled *bool) *httptest.ResponseRecorder {
	t.Helper()
	archiveBytes := testArchiveBytes(t, map[string][]byte{
		"Takeout/Google Photos/Photos from 2025/photo.jpg": []byte("photo"),
	})
	return performArchiveRequest(t, service, archiveBytes, momentsEnabled)
}

func performArchiveRequest(t *testing.T, service *Service, archiveBytes []byte, momentsEnabled *bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", "takeout.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archiveBytes); err != nil {
		t.Fatal(err)
	}
	if momentsEnabled != nil {
		if err := writer.WriteField("momentsEnabled", strconv.FormatBool(*momentsEnabled)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return performMultipartImportRequest(t, service, writer.FormDataContentType(), &body)
}

func performMultipartImportRequest(t *testing.T, service *Service, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/media/import/google-takeout", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = request
	ctx.Set(auth.UserContextKey, auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"})
	service.Import(ctx)
	entries, err := os.ReadDir(service.tmpDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary archive was not removed: entries=%v, error=%v", entries, err)
	}
	return response
}

func TestImportHandlerStagesArchiveInConfiguredTemporaryDirectory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newTestService(t, newRecordingRepository())
	staged := false
	service.diskFree = func(directory string) (int64, error) {
		if directory == service.tmpDir {
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".takeout-archive-") {
				t.Fatalf("archive not staged in configured directory: %v, %v", entries, err)
			}
			staged = true
		}
		return 1 << 30, nil
	}
	response := performImportRequest(t, service, nil)
	if response.Code != http.StatusCreated || !staged {
		t.Fatalf("archive was not streamed to configured directory: status=%d, staged=%t, body=%s", response.Code, staged, response.Body.String())
	}
}

func TestImportHandlerStreamsLargeArchivesWithoutOSTemporaryFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	service.maxArchiveSize = 64 << 20
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.CreateHeader(&zip.FileHeader{
		Name: "Takeout/Google Photos/Photos from 2025/photo.jpg", Method: zip.Store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(entry, strings.NewReader(strings.Repeat("x", 33<<20))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(service.tmpDir, "unavailable-os-temp"))
	response := performArchiveRequest(t, service, archive.Bytes(), nil)
	if response.Code != http.StatusCreated || len(repository.saved) != 1 || repository.saved[0].Size != 33<<20 {
		t.Fatalf("large import failed without OS temporary storage: status=%d, body=%s", response.Code, response.Body.String())
	}
}

func TestImportHandlerValidatesStreamingMultipartRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	archive := testArchiveBytes(t, map[string][]byte{
		"Takeout/Google Photos/Photos from 2025/photo.jpg": []byte("photo"),
	})
	for _, test := range []struct {
		name     string
		archives int
		value    string
		before   bool
		truncate bool
		status   int
	}{
		{name: "option before archive", archives: 1, value: "false", before: true, status: http.StatusCreated},
		{name: "invalid trailing option", archives: 1, value: "invalid", status: http.StatusBadRequest},
		{name: "oversized trailing option", archives: 1, value: strings.Repeat("x", 65), status: http.StatusBadRequest},
		{name: "duplicate archive", archives: 2, status: http.StatusBadRequest},
		{name: "missing archive", value: "false", status: http.StatusBadRequest},
		{name: "truncated body", archives: 1, truncate: true, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newRecordingRepository()
			service := newTestService(t, repository)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if test.before {
				if err := writer.WriteField("momentsEnabled", test.value); err != nil {
					t.Fatal(err)
				}
			}
			for index := 0; index < test.archives; index++ {
				part, err := writer.CreateFormFile("archive", "takeout.zip")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write(archive); err != nil {
					t.Fatal(err)
				}
			}
			if !test.before && test.value != "" {
				if err := writer.WriteField("momentsEnabled", test.value); err != nil {
					t.Fatal(err)
				}
			}
			if !test.truncate {
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			response := performMultipartImportRequest(t, service, writer.FormDataContentType(), &body)
			if response.Code != test.status {
				t.Fatalf("status=%d, want=%d: %s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusCreated {
				if len(repository.saved) != 1 || repository.saved[0].MomentsEnabled == nil || *repository.saved[0].MomentsEnabled {
					t.Fatal("option before archive did not disable Moments")
				}
			} else if len(repository.saved) != 0 {
				t.Fatal("invalid multipart request persisted media")
			}
		})
	}
}

func TestImportHandlerCleansUpStagedArchiveOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name      string
		archive   []byte
		limit     int64
		available int64
		status    int
	}{
		{name: "invalid zip", archive: []byte("not a zip"), limit: 100, available: 1000, status: http.StatusBadRequest},
		{name: "empty archive", limit: 100, available: 1000, status: http.StatusBadRequest},
		{name: "oversized archive", archive: []byte("too large"), limit: 4, available: 1000, status: http.StatusBadRequest},
		{name: "low space", archive: []byte("archive"), limit: 100, available: 0, status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newTestService(t, newRecordingRepository())
			service.maxArchiveSize = test.limit
			service.diskFree = func(string) (int64, error) { return test.available, nil }
			response := performArchiveRequest(t, service, test.archive, nil)
			if response.Code != test.status {
				t.Fatalf("status=%d, want=%d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestImportArchiveRejectsUnsafePaths(t *testing.T) {
	repository := newRecordingRepository()
	service := newTestService(t, repository)
	archive := testArchive(t, map[string][]byte{"../photo.jpg": []byte("photo")})
	_, err := service.importArchive(context.Background(), archive,
		auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"}, false)
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("error = %v, want invalid archive", err)
	}
}

func newTestService(t *testing.T, repository Repository) *Service {
	t.Helper()
	root := t.TempDir()
	service, err := New(repository, root+"/tmp", root+"/media", 10<<20, 100<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testArchive(t *testing.T, files map[string][]byte) *zip.Reader {
	t.Helper()
	data := testArchiveBytes(t, files)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func testArchiveBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
