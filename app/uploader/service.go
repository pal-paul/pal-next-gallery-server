package uploader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type service struct {
	tmpDir         string
	mediaDir       string
	maxUploadSize  int64
	maxPendingSize int64
	maxStorageSize int64
	minDiskFree    int64
	maxActive      int
	diskFree       func(string) (int64, error)
	mediaStore     UploadRepository
	uploads        map[string]*uploadSession
	uploadMux      sync.RWMutex
}

type IUploaderService interface {
	HandleCheckDuplicate(context *gin.Context)
	HandleCreateUpload(context *gin.Context)
	HandleUploadPart(context *gin.Context)
	HandleCompleteUpload(context *gin.Context)
	HandleGetUploadStatus(context *gin.Context)
	HandleDeleteUpload(context *gin.Context)
	CancelBatchUploads(context.Context, string, string) error
	CleanupExpiredUploads(time.Time, time.Duration) (int, error)
}

type Option func(*service) error

func WithMaxUploadSize(size int64) Option {
	return func(service *service) error {
		if size <= 0 {
			return fmt.Errorf("maximum upload size must be positive")
		}
		service.maxUploadSize = size
		return nil
	}
}

func WithMaxPendingUploadSize(size int64) Option {
	return func(service *service) error {
		if size <= 0 {
			return fmt.Errorf("maximum pending upload size must be positive")
		}
		service.maxPendingSize = size
		return nil
	}
}

func WithMaxUserStorageSize(size int64) Option {
	return func(service *service) error {
		if size <= 0 {
			return fmt.Errorf("maximum user storage size must be positive")
		}
		service.maxStorageSize = size
		return nil
	}
}

func WithMaxActiveUploads(count int) Option {
	return func(service *service) error {
		if count <= 0 {
			return fmt.Errorf("maximum active uploads must be positive")
		}
		service.maxActive = count
		return nil
	}
}

func WithMinDiskFree(size int64) Option {
	return func(service *service) error {
		if size < 0 {
			return fmt.Errorf("minimum disk free size cannot be negative")
		}
		service.minDiskFree = size
		return nil
	}
}

func withDiskFree(check func(string) (int64, error)) Option {
	return func(service *service) error {
		if check == nil {
			return fmt.Errorf("disk free check is required")
		}
		service.diskFree = check
		return nil
	}
}

func WithMediaRepository(repository UploadRepository) Option {
	return func(service *service) error {
		if repository == nil {
			return fmt.Errorf("media repository is required")
		}
		service.mediaStore = repository
		return nil
	}
}

func New(tmpDir string, mediaDir string, options ...Option) (IUploaderService, error) {
	if tmpDir == "" || mediaDir == "" {
		return nil, fmt.Errorf("temporary and media directories are required")
	}

	service := &service{
		tmpDir:         filepath.Clean(tmpDir),
		mediaDir:       filepath.Clean(mediaDir),
		maxUploadSize:  defaultMaxUploadSize,
		maxPendingSize: defaultMaxPendingSize,
		maxStorageSize: defaultMaxStorageSize,
		minDiskFree:    defaultMinDiskFree,
		maxActive:      defaultMaxActive,
		diskFree:       availableDiskBytes,
		uploads:        make(map[string]*uploadSession),
	}
	for _, option := range options {
		if err := option(service); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(service.tmpDir, 0750); err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	if err := os.MkdirAll(service.mediaDir, 0750); err != nil {
		return nil, fmt.Errorf("create media directory: %w", err)
	}
	if err := service.loadUploads(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *service) loadUploads() error {
	entries, err := os.ReadDir(s.tmpDir)
	if err != nil {
		return fmt.Errorf("read temporary directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metadataPath := filepath.Join(s.tmpDir, entry.Name(), metadataFilename)
		file, err := os.Open(metadataPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("open upload metadata %q: %w", entry.Name(), err)
		}
		var upload Upload
		decodeErr := json.NewDecoder(file).Decode(&upload)
		closeErr := file.Close()
		if decodeErr != nil {
			return fmt.Errorf("decode upload metadata %q: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close upload metadata %q: %w", entry.Name(), closeErr)
		}
		if upload.ID != entry.Name() || upload.Size <= 0 || upload.Size > s.maxUploadSize || upload.Chunks <= 0 {
			return fmt.Errorf("invalid upload metadata %q", entry.Name())
		}
		if s.mediaStore != nil {
			if err := s.mediaStore.RestoreUploadStorage(context.Background(), upload); err != nil {
				return fmt.Errorf("restore upload storage %q: %w", entry.Name(), err)
			}
		}
		s.uploads[upload.ID] = &uploadSession{upload: upload}
	}
	return nil
}

func (s *service) writeMetadata(upload *Upload) error {
	dir := filepath.Join(s.tmpDir, upload.ID)
	temporary, err := os.CreateTemp(dir, ".upload-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := json.NewEncoder(temporary).Encode(upload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(dir, metadataFilename)); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func (s *service) removeUpload(ctx context.Context, upload Upload) error {
	if s.mediaStore != nil {
		if err := s.mediaStore.ReleaseUploadStorage(ctx, upload.OwnerID, upload.ID); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(s.tmpDir, upload.ID)); err != nil {
		var restoreErr error
		if s.mediaStore != nil {
			restoreErr = s.mediaStore.RestoreUploadStorage(context.WithoutCancel(ctx), upload)
		}
		return errors.Join(err, restoreErr)
	}
	return nil
}

func (s *service) CancelBatchUploads(ctx context.Context, batchID, ownerID string) error {
	s.uploadMux.RLock()
	sessions := make(map[string]*uploadSession)
	for id, session := range s.uploads {
		if session.upload.BatchID == batchID && session.upload.OwnerID == ownerID {
			sessions[id] = session
		}
	}
	s.uploadMux.RUnlock()

	var cleanupErrors []error
	for id, session := range sessions {
		session.mutex.Lock()
		if !session.deleted {
			if err := s.removeUpload(ctx, session.upload); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("cancel upload %q: %w", id, err))
			} else {
				session.deleted = true
				s.uploadMux.Lock()
				delete(s.uploads, id)
				s.uploadMux.Unlock()
			}
		}
		session.mutex.Unlock()
	}
	return errors.Join(cleanupErrors...)
}

func (s *service) CleanupExpiredUploads(now time.Time, maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("upload retention must be positive")
	}
	s.uploadMux.RLock()
	sessions := make(map[string]*uploadSession, len(s.uploads))
	for id, session := range s.uploads {
		sessions[id] = session
	}
	s.uploadMux.RUnlock()

	removed := 0
	cutoff := now.Add(-maxAge)
	for id, session := range sessions {
		session.mutex.Lock()
		dir := filepath.Join(s.tmpDir, id)
		info, err := os.Stat(dir)
		if err != nil && !os.IsNotExist(err) {
			session.mutex.Unlock()
			return removed, fmt.Errorf("stat upload %q: %w", id, err)
		}
		if session.deleted || err == nil && !info.ModTime().Before(cutoff) {
			session.mutex.Unlock()
			continue
		}
		if err := s.removeUpload(context.Background(), session.upload); err != nil {
			session.mutex.Unlock()
			return removed, fmt.Errorf("remove expired upload %q: %w", id, err)
		}
		session.deleted = true
		s.uploadMux.Lock()
		if s.uploads[id] == session {
			delete(s.uploads, id)
			removed++
		}
		s.uploadMux.Unlock()
		session.mutex.Unlock()
	}
	return removed, nil
}
