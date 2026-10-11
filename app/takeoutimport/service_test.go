package takeoutimport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"pal-next-gallery-server/app/auth"
	"pal-next-gallery-server/app/uploader"

	"github.com/gin-gonic/gin"
)

type recordingRepository struct {
	media  map[string]uploader.CompletedMedia
	saved  []uploader.CompletedMedia
	albums map[string][]string
}

func newRecordingRepository() *recordingRepository {
	return &recordingRepository{media: make(map[string]uploader.CompletedMedia), albums: make(map[string][]string)}
}

func (repository *recordingRepository) SaveCompletedMedia(_ context.Context, media uploader.CompletedMedia) error {
	if _, duplicate := repository.media[media.SHA256]; duplicate {
		return uploader.ErrDuplicateMedia
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

func performImportRequest(t *testing.T, service *Service, momentsEnabled *bool) *httptest.ResponseRecorder {
	t.Helper()
	archiveBytes := testArchiveBytes(t, map[string][]byte{
		"Takeout/Google Photos/Photos from 2025/photo.jpg": []byte("photo"),
	})
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

	request := httptest.NewRequest(http.MethodPost, "/media/import/google-takeout", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = request
	ctx.Set(auth.UserContextKey, auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"})
	service.Import(ctx)
	return response
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
	service, err := New(repository, root+"/tmp", root+"/media", 10<<20, 100<<20)
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
