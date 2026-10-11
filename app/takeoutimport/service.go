package takeoutimport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pal-next-gallery-server/app/auth"
	"pal-next-gallery-server/app/uploader"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	maxSidecarSize  = 2 << 20
	maxArchiveFiles = 100000
)

var (
	ErrInvalidArchive = errors.New("invalid Google Takeout archive")
	ErrStorageQuota   = errors.New("storage quota exceeded")
)

type Repository interface {
	uploader.MediaRepository
	UpsertTakeoutAlbum(context.Context, string, string, string, []string) error
}

type Service struct {
	repository     Repository
	tmpDir         string
	mediaDir       string
	maxArchiveSize int64
	maxStorageSize int64
}

type Result struct {
	Imported       int  `json:"imported"`
	Duplicates     int  `json:"duplicates"`
	Skipped        int  `json:"skipped"`
	Albums         int  `json:"albums"`
	MomentsEnabled bool `json:"momentsEnabled"`
}

type sidecar struct {
	Title          string `json:"title"`
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
}

func New(repository Repository, tmpDir, mediaDir string, maxArchiveSize, maxStorageSize int64) (*Service, error) {
	if repository == nil {
		return nil, errors.New("takeout repository is required")
	}
	if strings.TrimSpace(tmpDir) == "" || strings.TrimSpace(mediaDir) == "" {
		return nil, errors.New("temporary and media directories are required")
	}
	if maxArchiveSize <= 0 || maxStorageSize <= 0 {
		return nil, errors.New("takeout size limits must be positive")
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, fmt.Errorf("create Takeout temporary directory: %w", err)
	}
	if err := os.MkdirAll(mediaDir, 0o750); err != nil {
		return nil, fmt.Errorf("create media directory: %w", err)
	}
	return &Service{
		repository: repository, tmpDir: filepath.Clean(tmpDir), mediaDir: filepath.Clean(mediaDir),
		maxArchiveSize: maxArchiveSize, maxStorageSize: maxStorageSize,
	}, nil
}

func RegisterRoutes(router gin.IRoutes, service *Service) {
	router.POST("/media/import/google-takeout", service.Import)
}

func (service *Service) Import(ctx *gin.Context) {
	user, ok := currentUser(ctx)
	if !ok || user.Role != "user" || strings.TrimSpace(user.UploadFolder) == "" {
		ctx.JSON(http.StatusForbidden, gin.H{"error": "Takeout imports require a regular user with an upload folder"})
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, service.maxArchiveSize+(1<<20))
	if err := ctx.Request.ParseMultipartForm(32 << 20); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid or oversized multipart request"})
		return
	}
	if ctx.Request.MultipartForm != nil {
		defer func() { _ = ctx.Request.MultipartForm.RemoveAll() }()
	}
	momentsEnabled := true
	if raw := strings.TrimSpace(ctx.Request.FormValue("momentsEnabled")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "momentsEnabled must be true or false"})
			return
		}
		momentsEnabled = parsed
	}
	archiveHeader, err := ctx.FormFile("archive")
	if err != nil || archiveHeader.Size <= 0 || archiveHeader.Size > service.maxArchiveSize {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "a non-empty Takeout ZIP archive is required"})
		return
	}
	archiveFile, err := archiveHeader.Open()
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "open Takeout archive failed"})
		return
	}
	defer archiveFile.Close()
	archive, err := zip.NewReader(archiveFile, archiveHeader.Size)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": ErrInvalidArchive.Error()})
		return
	}
	result, err := service.importArchive(ctx.Request.Context(), archive, user, momentsEnabled)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrInvalidArchive) {
			status = http.StatusBadRequest
		} else if errors.Is(err, ErrStorageQuota) {
			status = http.StatusRequestEntityTooLarge
		}
		ctx.JSON(status, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusCreated, result)
}

func (service *Service) importArchive(ctx context.Context, archive *zip.Reader, user auth.User, momentsEnabled bool) (Result, error) {
	result := Result{MomentsEnabled: momentsEnabled}
	if len(archive.File) == 0 || len(archive.File) > maxArchiveFiles {
		return result, ErrInvalidArchive
	}
	// maxArchiveSize is positive and bounded by the signed configuration type.
	expandedLimit := uint64(service.maxArchiveSize) * 2 // #nosec G115
	var expandedSize uint64
	sidecars := make(map[string]sidecar)
	for _, entry := range archive.File {
		cleanName, err := cleanArchivePath(entry.Name)
		if err != nil {
			return result, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.UncompressedSize64 > expandedLimit-expandedSize {
			return result, fmt.Errorf("%w: expanded content is too large", ErrInvalidArchive)
		}
		expandedSize += entry.UncompressedSize64
		if strings.EqualFold(path.Ext(cleanName), ".json") {
			metadata, ok, err := readSidecar(entry)
			if err != nil {
				return result, err
			}
			if ok {
				baseKey := strings.TrimSuffix(cleanName, path.Ext(cleanName))
				sidecars[strings.ToLower(baseKey)] = metadata
				if strings.TrimSpace(metadata.Title) != "" {
					titleKey := path.Join(path.Dir(cleanName), path.Base(metadata.Title))
					sidecars[strings.ToLower(titleKey)] = metadata
				}
			}
		}
	}

	storedBytes, err := service.repository.OwnerStorageBytes(ctx, user.ID)
	if err != nil {
		return result, fmt.Errorf("read storage usage: %w", err)
	}
	storageQuota := service.maxStorageSize
	if quota, configured, err := service.repository.OwnerStorageQuota(ctx, user.ID); err != nil {
		return result, fmt.Errorf("read storage quota: %w", err)
	} else if configured {
		storageQuota = quota
	}
	albums := make(map[string]map[string]struct{})
	albumTitles := make(map[string]string)
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		cleanName, _ := cleanArchivePath(entry.Name)
		relativeName, ok := googlePhotosRelative(cleanName)
		if entry.FileInfo().IsDir() || !ok || !supportedMediaExtension(path.Ext(relativeName)) {
			if !entry.FileInfo().IsDir() && !strings.EqualFold(path.Ext(cleanName), ".json") {
				result.Skipped++
			}
			continue
		}
		metadata := sidecars[strings.ToLower(cleanName)]
		capturedAt := sidecarCaptureTime(metadata)
		media, duplicate, err := service.importMedia(ctx, entry, user, capturedAt, momentsEnabled, storedBytes, storageQuota)
		if err != nil {
			return result, fmt.Errorf("import %q: %w", cleanName, err)
		}
		if duplicate {
			result.Duplicates++
		} else {
			result.Imported++
			storedBytes += media.Size
		}
		if sourceKey, title, ok := takeoutAlbum(relativeName); ok {
			if albums[sourceKey] == nil {
				albums[sourceKey] = make(map[string]struct{})
			}
			albums[sourceKey][media.UploadID] = struct{}{}
			albumTitles[sourceKey] = title
		}
	}
	for sourceKey, members := range albums {
		mediaIDs := make([]string, 0, len(members))
		for mediaID := range members {
			mediaIDs = append(mediaIDs, mediaID)
		}
		if err := service.repository.UpsertTakeoutAlbum(ctx, user.ID, sourceKey, albumTitles[sourceKey], mediaIDs); err != nil {
			return result, fmt.Errorf("save Takeout album %q: %w", albumTitles[sourceKey], err)
		}
		result.Albums++
	}
	return result, nil
}

func (service *Service) importMedia(ctx context.Context, entry *zip.File, user auth.User, capturedAt *time.Time,
	momentsEnabled bool, storedBytes, storageQuota int64) (uploader.CompletedMedia, bool, error) {
	createdAt := entry.Modified.UTC()
	if capturedAt != nil {
		createdAt = *capturedAt
	} else if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	uploadID := uuid.NewString()
	filename := path.Base(entry.Name)
	relativeDestination := filepath.Join(user.UploadFolder, createdAt.Format("2006"), createdAt.Format("01"), uploadID+"_"+filename)
	destination := filepath.Join(service.mediaDir, relativeDestination)
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	source, err := entry.Open()
	if err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".takeout-import-*.tmp")
	if err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
			_ = os.Remove(destination)
		}
	}()

	hash := sha256.New()
	sniff := make([]byte, 512)
	sniffed, readErr := io.ReadFull(source, sniff)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return uploader.CompletedMedia{}, false, readErr
	}
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.MultiReader(bytes.NewReader(sniff[:sniffed]), source))
	if err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	if written <= 0 || uint64(written) != entry.UncompressedSize64 {
		return uploader.CompletedMedia{}, false, fmt.Errorf("%w: media size mismatch", ErrInvalidArchive)
	}
	if err := temporary.Sync(); err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	if err := temporary.Close(); err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if existing, duplicate, err := service.repository.FindOwnedMediaBySHA256(ctx, user.ID, checksum); err != nil {
		return uploader.CompletedMedia{}, false, err
	} else if duplicate {
		return existing, true, nil
	}
	if written > storageQuota-storedBytes {
		return uploader.CompletedMedia{}, false, ErrStorageQuota
	}
	mimeType := mime.TypeByExtension(strings.ToLower(path.Ext(filename)))
	if mimeType == "" {
		mimeType = http.DetectContentType(sniff[:sniffed])
	}
	if !strings.HasPrefix(mimeType, "image/") && !strings.HasPrefix(mimeType, "video/") {
		return uploader.CompletedMedia{}, false, fmt.Errorf("%w: unsupported media type", ErrInvalidArchive)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return uploader.CompletedMedia{}, false, err
	}
	momentsSetting := momentsEnabled
	media := uploader.CompletedMedia{
		UploadID: uploadID, OwnerID: user.ID, Filename: filename, MimeType: mimeType, Size: written,
		SHA256: checksum, MediaPath: filepath.ToSlash(relativeDestination), CreatedAt: createdAt,
		CapturedAt: capturedAt, MomentsEnabled: &momentsSetting,
	}
	if err := service.repository.SaveCompletedMedia(ctx, media); err != nil {
		if errors.Is(err, uploader.ErrDuplicateMedia) {
			existing, found, findErr := service.repository.FindOwnedMediaBySHA256(ctx, user.ID, checksum)
			if findErr != nil {
				return uploader.CompletedMedia{}, false, findErr
			}
			if found {
				return existing, true, nil
			}
		}
		return uploader.CompletedMedia{}, false, err
	}
	committed = true
	return media, false, nil
}

func cleanArchivePath(name string) (string, error) {
	if strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') {
		return "", ErrInvalidArchive
	}
	cleanName := path.Clean(strings.TrimPrefix(name, "./"))
	if cleanName == "." || path.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return "", ErrInvalidArchive
	}
	return cleanName, nil
}

func googlePhotosRelative(name string) (string, bool) {
	parts := strings.Split(name, "/")
	for index, part := range parts {
		if strings.EqualFold(part, "Google Photos") && index+1 < len(parts) {
			return strings.Join(parts[index+1:], "/"), true
		}
	}
	return "", false
}

func supportedMediaExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".tif", ".tiff",
		".mp4", ".mov", ".m4v", ".avi", ".mkv", ".webm", ".3gp":
		return true
	default:
		return false
	}
}

func readSidecar(entry *zip.File) (sidecar, bool, error) {
	if entry.UncompressedSize64 > maxSidecarSize {
		return sidecar{}, false, nil
	}
	reader, err := entry.Open()
	if err != nil {
		return sidecar{}, false, err
	}
	defer reader.Close()
	var metadata sidecar
	if err := json.NewDecoder(io.LimitReader(reader, maxSidecarSize)).Decode(&metadata); err != nil {
		return sidecar{}, false, nil
	}
	return metadata, metadata.Title != "" || metadata.PhotoTakenTime.Timestamp != "", nil
}

func sidecarCaptureTime(metadata sidecar) *time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(metadata.PhotoTakenTime.Timestamp), 10, 64)
	if err != nil || seconds <= 0 {
		return nil
	}
	capturedAt := time.Unix(seconds, 0).UTC()
	return &capturedAt
}

func takeoutAlbum(relativeName string) (string, string, bool) {
	directory := path.Dir(relativeName)
	if directory == "." || strings.HasPrefix(strings.ToLower(directory), "photos from ") {
		return "", "", false
	}
	title := strings.TrimSpace(path.Base(directory))
	if title == "" || title == "." {
		return "", "", false
	}
	return strings.ToLower(directory), title, true
}

func currentUser(ctx *gin.Context) (auth.User, bool) {
	value, exists := ctx.Get(auth.UserContextKey)
	user, ok := value.(auth.User)
	return user, exists && ok
}
