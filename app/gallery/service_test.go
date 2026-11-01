package gallery

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
)

type testRepository struct {
	createdAlbum Album
	mediaPaths   MediaPaths
	mediaFilter  MediaFilter
	deletedID    string
	deletedOwner string
	deleteErr    error
}

func (repository *testRepository) ListAlbums(context.Context, string, string, ...Page) ([]Album, error) {
	return nil, nil
}
func (repository *testRepository) CreateAlbum(_ context.Context, album Album) error {
	repository.createdAlbum = album
	return nil
}
func (repository *testRepository) GetAlbum(context.Context, string, string, ...Page) (Album, error) {
	return repository.createdAlbum, nil
}
func (repository *testRepository) UpdateAlbum(context.Context, string, string, string, string) error {
	return nil
}
func (repository *testRepository) DeleteAlbum(context.Context, string, string) error { return nil }
func (repository *testRepository) SetAlbumOrder(context.Context, string, []string) error {
	return nil
}
func (repository *testRepository) AddAlbumMedia(context.Context, string, string, []string) error {
	return nil
}
func (repository *testRepository) RemoveAlbumMedia(context.Context, string, string, string) error {
	return nil
}
func (repository *testRepository) SetAlbumCover(context.Context, string, string, string) error {
	return nil
}
func (repository *testRepository) ListMedia(_ context.Context, _ string, filter MediaFilter) ([]Media, error) {
	repository.mediaFilter = filter
	return nil, nil
}
func (repository *testRepository) SetFavorite(context.Context, string, string, bool) error {
	return nil
}
func (repository *testRepository) TrashMedia(context.Context, string, string) error { return nil }
func (repository *testRepository) RestoreMedia(context.Context, string, string) error {
	return nil
}
func (repository *testRepository) GetOwnedMediaPaths(context.Context, string, string) (MediaPaths, error) {
	return repository.mediaPaths, nil
}
func (repository *testRepository) DeleteMedia(_ context.Context, mediaID, ownerID string) error {
	repository.deletedID = mediaID
	repository.deletedOwner = ownerID
	return repository.deleteErr
}
func (repository *testRepository) GetStorage(context.Context, string) (Storage, error) {
	return Storage{}, nil
}

func TestCreateAlbumUsesAuthenticatedOwner(t *testing.T) {
	repository := &testRepository{}
	router := testRouter(repository, t.TempDir())
	request := httptest.NewRequest(http.MethodPost, "/albums", bytes.NewBufferString(`{"title":"  Holiday  ","description":"  Coast  "}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, response.Code, response.Body.String())
	}
	if repository.createdAlbum.OwnerID != "user-1" {
		t.Fatalf("expected authenticated owner, got %q", repository.createdAlbum.OwnerID)
	}
	if repository.createdAlbum.Title != "Holiday" || repository.createdAlbum.Description != "Coast" {
		t.Fatalf("expected trimmed album fields, got %#v", repository.createdAlbum)
	}
}

func TestDeleteMediaRemovesOwnedFile(t *testing.T) {
	mediaDir := t.TempDir()
	relativePath := filepath.Join("alice", "video.mp4")
	path := filepath.Join(mediaDir, relativePath)
	thumbnailRelativePath := filepath.Join(".thumbnails", "user-1", "media-1.jpg")
	thumbnailPath := filepath.Join(mediaDir, thumbnailRelativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(thumbnailPath), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbnailPath, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	repository := &testRepository{mediaPaths: MediaPaths{Media: relativePath, Thumbnail: thumbnailRelativePath}}
	router := testRouter(repository, mediaDir)
	request := httptest.NewRequest(http.MethodDelete, "/media/files/media-1/permanent", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNoContent, response.Code, response.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected media file to be removed, got %v", err)
	}
	if _, err := os.Stat(thumbnailPath); !os.IsNotExist(err) {
		t.Fatalf("expected thumbnail file to be removed, got %v", err)
	}
	if repository.deletedID != "media-1" || repository.deletedOwner != "user-1" {
		t.Fatalf("unexpected delete scope: media=%q owner=%q", repository.deletedID, repository.deletedOwner)
	}
}

func TestDeleteMediaRestoresFilesWhenDatabaseDeleteFails(t *testing.T) {
	mediaDir := t.TempDir()
	relativePath := filepath.Join("alice", "video.mp4")
	thumbnailRelativePath := filepath.Join(".thumbnails", "user-1", "media-1.jpg")
	for path, content := range map[string]string{
		relativePath: "media", thumbnailRelativePath: "thumbnail",
	} {
		absolutePath := filepath.Join(mediaDir, path)
		if err := os.MkdirAll(filepath.Dir(absolutePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolutePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repository := &testRepository{
		mediaPaths: MediaPaths{Media: relativePath, Thumbnail: thumbnailRelativePath},
		deleteErr:  errors.New("database unavailable"),
	}
	router := testRouter(repository, mediaDir)
	request := httptest.NewRequest(http.MethodDelete, "/media/files/media-1/permanent", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d: %s", http.StatusInternalServerError, response.Code, response.Body.String())
	}
	for _, path := range []string{relativePath, thumbnailRelativePath} {
		if _, err := os.Stat(filepath.Join(mediaDir, path)); err != nil {
			t.Fatalf("expected %s to be restored: %v", path, err)
		}
	}
}

func TestListMediaPreservesEqualCaptureBounds(t *testing.T) {
	repository := &testRepository{}
	router := testRouter(repository, t.TempDir())
	request := httptest.NewRequest(http.MethodGet, "/media?capturedAfter=2026-10-01T00:00:00Z&capturedBefore=2026-10-01T00:00:00Z", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if repository.mediaFilter.CapturedAfter == nil || repository.mediaFilter.CapturedBefore == nil {
		t.Fatalf("expected both capture bounds, got %#v", repository.mediaFilter)
	}
}

func TestListMediaPagination(t *testing.T) {
	for _, scenario := range []struct {
		query         string
		status        int
		limit, offset int
	}{
		{"", 200, 0, 0},
		{"?limit=60&offset=120&favorites=true&sort=oldest", 200, 60, 120},
		{"?limit=0", 400, 0, 0},
		{"?limit=201", 400, 0, 0},
		{"?limit=60&offset=-1", 400, 0, 0},
		{"?offset=1", 400, 0, 0},
	} {
		t.Run(scenario.query, func(t *testing.T) {
			repository := &testRepository{}
			response := httptest.NewRecorder()
			testRouter(repository, t.TempDir()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/media"+scenario.query, nil))
			if response.Code != scenario.status || repository.mediaFilter.Limit != scenario.limit || repository.mediaFilter.Offset != scenario.offset {
				t.Fatalf("status=%d filter=%+v", response.Code, repository.mediaFilter)
			}
			if scenario.limit > 0 && (!repository.mediaFilter.Favorites || repository.mediaFilter.Sort != "oldest") {
				t.Fatal("lost query filters")
			}
		})
	}
}

func testRouter(repository Repository, mediaDir string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(context *gin.Context) {
		context.Set(auth.UserContextKey, auth.User{ID: "user-1", Role: "user", UploadFolder: "alice"})
	})
	RegisterRoutes(router, NewService(repository, mediaDir))
	return router
}
