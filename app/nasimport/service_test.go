package nasimport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pal-next-gallery-server/app/uploader"
)

type recordingRepository struct {
	users []User
	media []uploader.CompletedMedia
	err   error
}

func (repository *recordingRepository) ListNASImportUsers(context.Context) ([]User, error) {
	return repository.users, nil
}

func (repository *recordingRepository) SaveCompletedMedia(_ context.Context, media uploader.CompletedMedia) error {
	repository.media = append(repository.media, media)
	return repository.err
}

func TestRunMovesMediaForUploadFolderOwner(t *testing.T) {
	importDir, mediaDir := t.TempDir(), t.TempDir()
	sourceDir := filepath.Join(importDir, "alice", "phone")
	if err := os.MkdirAll(sourceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "photo.jpg")
	content := []byte("existing-media")
	if err := os.WriteFile(source, content, 0o640); err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{users: []User{{ID: "user-1", UploadFolder: "alice"}}}
	service, err := New(repository, importDir, mediaDir)
	if err != nil {
		t.Fatal(err)
	}

	count, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(repository.media) != 1 {
		t.Fatalf("imported %d files and saved %d media, want 1", count, len(repository.media))
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source was not removed: %v", err)
	}
	media := repository.media[0]
	if media.OwnerID != "user-1" || media.Filename != "photo.jpg" || media.SHA256 == "" {
		t.Fatalf("unexpected media: %#v", media)
	}
	stored, err := os.ReadFile(filepath.Join(mediaDir, filepath.FromSlash(media.MediaPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) {
		t.Fatalf("stored content = %q", stored)
	}
}

func TestRunLeavesDuplicateUploadFolderUnassigned(t *testing.T) {
	importDir, mediaDir := t.TempDir(), t.TempDir()
	sourceDir := filepath.Join(importDir, "shared")
	if err := os.MkdirAll(sourceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "photo.jpg")
	if err := os.WriteFile(source, []byte("ambiguous-media"), 0o640); err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{users: []User{
		{ID: "user-1", UploadFolder: "shared"},
		{ID: "user-2", UploadFolder: "shared"},
		{ID: "user-3", UploadFolder: "shared"},
	}}
	service, err := New(repository, importDir, mediaDir)
	if err != nil {
		t.Fatal(err)
	}

	count, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(repository.media) != 0 {
		t.Fatalf("imported %d files and saved %d media, want 0", count, len(repository.media))
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("ambiguous source should remain: %v", err)
	}
}

func TestRunKeepsSourceWhenSaveFails(t *testing.T) {
	importDir, mediaDir := t.TempDir(), t.TempDir()
	source := filepath.Join(importDir, "photo.jpg")
	if err := os.WriteFile(source, []byte("media"), 0o640); err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{users: []User{{ID: "user-1", UploadFolder: "alice"}}, err: errors.New("database unavailable")}
	service, _ := New(repository, importDir, mediaDir)

	if _, err := service.Run(context.Background()); err == nil {
		t.Fatal("expected import failure")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source should remain for retry: %v", err)
	}
}

func TestRunRemovesDuplicateSource(t *testing.T) {
	importDir, mediaDir := t.TempDir(), t.TempDir()
	source := filepath.Join(importDir, "photo.jpg")
	if err := os.WriteFile(source, []byte("duplicate"), 0o640); err != nil {
		t.Fatal(err)
	}
	repository := &recordingRepository{users: []User{{ID: "user-1", UploadFolder: "alice"}}, err: uploader.ErrDuplicateMedia}
	service, _ := New(repository, importDir, mediaDir)

	if count, err := service.Run(context.Background()); err != nil || count != 1 {
		t.Fatalf("Run() = %d, %v", count, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("duplicate source was not removed: %v", err)
	}
}
