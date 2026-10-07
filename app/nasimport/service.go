package nasimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pal-next-gallery-server/app/autoalbum"
	"pal-next-gallery-server/app/uploader"

	"github.com/google/uuid"
)

type User struct {
	ID           string
	UploadFolder string
}

type Repository interface {
	ListNASImportUsers(context.Context) ([]User, error)
	SaveCompletedMedia(context.Context, uploader.CompletedMedia) error
}

type Service struct {
	repository Repository
	importDir  string
	mediaDir   string
}

func New(repository Repository, importDir, mediaDir string) (*Service, error) {
	if repository == nil {
		return nil, errors.New("NAS import repository is required")
	}
	if strings.TrimSpace(importDir) == "" || strings.TrimSpace(mediaDir) == "" {
		return nil, errors.New("NAS import and media directories are required")
	}
	return &Service{repository: repository, importDir: filepath.Clean(importDir), mediaDir: filepath.Clean(mediaDir)}, nil
}

func (service *Service) Run(ctx context.Context) (int, error) {
	users, err := service.repository.ListNASImportUsers(ctx)
	if err != nil {
		return 0, err
	}
	byFolder := make(map[string]User, len(users))
	ambiguousFolders := make(map[string]struct{})
	for _, user := range users {
		folder := filepath.Clean(user.UploadFolder)
		if user.ID != "" && folder != "." && !filepath.IsAbs(folder) && !strings.HasPrefix(folder, ".."+string(filepath.Separator)) {
			if _, ambiguous := ambiguousFolders[folder]; ambiguous {
				continue
			}
			if _, exists := byFolder[folder]; exists {
				delete(byFolder, folder)
				ambiguousFolders[folder] = struct{}{}
				continue
			}
			byFolder[folder] = user
		}
	}

	imported := 0
	err = filepath.WalkDir(service.importDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(service.importDir, path)
		if err != nil {
			return err
		}
		user, ok := ownerFor(relative, users, byFolder)
		if !ok {
			return nil
		}
		if err := service.importFile(ctx, path, info, user); err != nil {
			return fmt.Errorf("import %q: %w", relative, err)
		}
		imported++
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return imported, err
}

func ownerFor(relative string, users []User, byFolder map[string]User) (User, bool) {
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if len(parts) > 1 {
		if user, ok := byFolder[parts[0]]; ok {
			return user, true
		}
	}
	if len(users) == 1 && users[0].ID != "" {
		return users[0], true
	}
	return User{}, false
}

func (service *Service) importFile(ctx context.Context, source string, info os.FileInfo, user User) error {
	uploadID := uuid.NewString()
	filename := filepath.Base(source)
	createdAt := info.ModTime().UTC()
	relativeDestination := filepath.Join(user.UploadFolder, createdAt.Format("2006"), createdAt.Format("01"), uploadID+"_"+filename)
	destination := filepath.Join(service.mediaDir, relativeDestination)
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}

	input, err := os.Open(source)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".nas-import-*.tmp")
	if err != nil {
		_ = input.Close()
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = input.Close()
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
			_ = os.Remove(destination)
		}
	}()

	hash := sha256.New()
	sniff := make([]byte, 512)
	sniffed, readErr := io.ReadFull(input, sniff)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return readErr
	}
	if _, err := io.Copy(io.MultiWriter(temporary, hash), io.MultiReader(bytes.NewReader(sniff[:sniffed]), input)); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := input.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if mimeType == "" {
		mimeType = http.DetectContentType(sniff[:sniffed])
	}
	media := uploader.CompletedMedia{
		UploadID: uploadID, OwnerID: user.ID, Filename: filename, MimeType: mimeType,
		Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil)),
		MediaPath: filepath.ToSlash(relativeDestination), CreatedAt: createdAt,
	}
	if err := service.repository.SaveCompletedMedia(ctx, media); err != nil {
		if errors.Is(err, uploader.ErrDuplicateMedia) {
			if removeErr := os.Remove(source); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
			return nil
		}
		return err
	}
	if err := os.Remove(source); err != nil {
		return err
	}
	committed = true
	return nil
}

func (service *Service) RunScheduled(ctx context.Context, schedule autoalbum.Schedule, onError func(error)) {
	for {
		timer := time.NewTimer(time.Until(schedule.NextRun(time.Now())))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			if _, err := service.Run(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
