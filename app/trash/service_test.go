package trash

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type cleanupRepository struct {
	items   []ExpiredMedia
	deleted []string
}

func (repository *cleanupRepository) ListExpiredTrash(context.Context, time.Time, int) ([]ExpiredMedia, error) {
	return repository.items, nil
}

func (repository *cleanupRepository) DeleteExpiredMedia(_ context.Context, mediaID string) error {
	repository.deleted = append(repository.deleted, mediaID)
	return nil
}

func TestCleanupRemovesExpiredMediaAndThumbnail(t *testing.T) {
	mediaDir := t.TempDir()
	mediaPath := filepath.Join("owner", "photo.jpg")
	thumbnailPath := filepath.Join("owner", "photo-thumb.jpg")
	for _, relativePath := range []string{mediaPath, thumbnailPath} {
		absolutePath := filepath.Join(mediaDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(absolutePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolutePath, []byte("content"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	repository := &cleanupRepository{items: []ExpiredMedia{{ID: "media-id", MediaPath: mediaPath, ThumbnailPath: thumbnailPath}}}

	removed, err := New(repository, mediaDir).Cleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(repository.deleted) != 1 || repository.deleted[0] != "media-id" {
		t.Fatalf("removed=%d deleted=%v", removed, repository.deleted)
	}
	for _, relativePath := range []string{mediaPath, thumbnailPath} {
		if _, err := os.Stat(filepath.Join(mediaDir, relativePath)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got %v", relativePath, err)
		}
	}
}
