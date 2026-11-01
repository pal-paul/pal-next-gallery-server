package gallery

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	ErrNotFound  = errors.New("resource not found")
	ErrForbidden = errors.New("operation not permitted")
)

type Service struct {
	repository Repository
	mediaDir   string
}

func NewService(repository Repository, mediaDir string) *Service {
	return &Service{repository: repository, mediaDir: filepath.Clean(mediaDir)}
}

func RegisterRoutes(router gin.IRoutes, service *Service) {
	router.GET("/albums", service.ListAlbums)
	router.POST("/albums", service.CreateAlbum)
	router.PATCH("/albums/order", service.SetAlbumOrder)
	router.GET("/albums/:id", service.GetAlbum)
	router.PATCH("/albums/:id", service.UpdateAlbum)
	router.DELETE("/albums/:id", service.DeleteAlbum)
	router.POST("/albums/:id/media", service.AddAlbumMedia)
	router.DELETE("/albums/:id/media/:mediaID", service.RemoveAlbumMedia)
	router.PATCH("/albums/:id/cover", service.SetAlbumCover)
	router.GET("/media", service.ListMedia)
	router.PATCH("/media/files/:id/favorite", service.SetFavorite)
	router.DELETE("/media/files/:id", service.TrashMedia)
	router.PATCH("/media/files/:id/restore", service.RestoreMedia)
	router.DELETE("/media/files/:id/permanent", service.DeleteMedia)
	router.GET("/storage", service.GetStorage)
}

func (service *Service) ListAlbums(context *gin.Context) {
	page, valid := ParsePage(context)
	if !valid {
		return
	}
	user := currentUser(context)
	albums, err := service.repository.ListAlbums(context, user.ID, context.Query("search"), page)
	respond(context, albums, err)
}

func (service *Service) CreateAlbum(context *gin.Context) {
	user := currentUser(context)
	var request struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if decodeJSON(context, &request) != nil || strings.TrimSpace(request.Title) == "" {
		context.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	album := Album{ID: uuid.NewString(), OwnerID: user.ID, Title: strings.TrimSpace(request.Title), Description: strings.TrimSpace(request.Description)}
	if err := service.repository.CreateAlbum(context, album); err != nil {
		respond(context, nil, err)
		return
	}
	created, err := service.repository.GetAlbum(context, album.ID, user.ID)
	if err != nil {
		respond(context, nil, err)
		return
	}
	context.JSON(http.StatusCreated, created)
}

func (service *Service) SetAlbumOrder(context *gin.Context) {
	var request struct {
		AlbumIDs []string `json:"albumIds"`
	}
	if decodeJSON(context, &request) != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid album order"})
		return
	}
	respondNoContent(context, service.repository.SetAlbumOrder(context, currentUser(context).ID, request.AlbumIDs))
}

func (service *Service) GetAlbum(context *gin.Context) {
	page, valid := ParsePage(context)
	if !valid {
		return
	}
	album, err := service.repository.GetAlbum(context, context.Param("id"), currentUser(context).ID, page)
	respond(context, album, err)
}

func (service *Service) UpdateAlbum(context *gin.Context) {
	var request struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if decodeJSON(context, &request) != nil || strings.TrimSpace(request.Title) == "" {
		context.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	err := service.repository.UpdateAlbum(context, context.Param("id"), currentUser(context).ID, strings.TrimSpace(request.Title), strings.TrimSpace(request.Description))
	respondNoContent(context, err)
}

func (service *Service) DeleteAlbum(context *gin.Context) {
	respondNoContent(context, service.repository.DeleteAlbum(context, context.Param("id"), currentUser(context).ID))
}

func (service *Service) AddAlbumMedia(context *gin.Context) {
	var request struct {
		MediaIDs []string `json:"mediaIds"`
	}
	if decodeJSON(context, &request) != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid media list"})
		return
	}
	err := service.repository.AddAlbumMedia(context, context.Param("id"), currentUser(context).ID, request.MediaIDs)
	respondNoContent(context, err)
}

func (service *Service) RemoveAlbumMedia(context *gin.Context) {
	err := service.repository.RemoveAlbumMedia(context, context.Param("id"), currentUser(context).ID, context.Param("mediaID"))
	respondNoContent(context, err)
}

func (service *Service) SetAlbumCover(context *gin.Context) {
	var request struct {
		MediaID string `json:"mediaId"`
	}
	if decodeJSON(context, &request) != nil || request.MediaID == "" {
		context.JSON(http.StatusBadRequest, gin.H{"error": "mediaId is required"})
		return
	}
	err := service.repository.SetAlbumCover(context, context.Param("id"), currentUser(context).ID, request.MediaID)
	respondNoContent(context, err)
}

func (service *Service) ListMedia(context *gin.Context) {
	page, valid := ParsePage(context)
	if !valid {
		return
	}
	filter := MediaFilter{Page: page, Favorites: context.Query("favorites") == "true", Search: context.Query("search"), Kind: context.Query("kind"), Sort: context.Query("sort"), Trash: context.Query("trash") == "true"}
	for _, bound := range []struct {
		value  string
		target **time.Time
	}{
		{value: context.Query("capturedAfter"), target: &filter.CapturedAfter},
		{value: context.Query("capturedBefore"), target: &filter.CapturedBefore},
	} {
		value := bound.value
		if value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				context.JSON(http.StatusBadRequest, gin.H{"error": "capture dates must use RFC3339"})
				return
			}
			*bound.target = &parsed
		}
	}
	for name, target := range map[string]**float64{"latitude": &filter.Latitude, "longitude": &filter.Longitude, "radiusKm": &filter.RadiusKM} {
		if value := context.Query(name); value != "" {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				context.JSON(http.StatusBadRequest, gin.H{"error": "location filters must be numbers"})
				return
			}
			*target = &parsed
		}
	}
	media, err := service.repository.ListMedia(context, currentUser(context).ID, filter)
	respond(context, media, err)
}

func (service *Service) SetFavorite(context *gin.Context) {
	var request struct {
		Favorite bool `json:"favorite"`
	}
	if decodeJSON(context, &request) != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid favorite state"})
		return
	}
	err := service.repository.SetFavorite(context, context.Param("id"), currentUser(context).ID, request.Favorite)
	respondNoContent(context, err)
}

func (service *Service) TrashMedia(context *gin.Context) {
	err := service.repository.TrashMedia(context, context.Param("id"), currentUser(context).ID)
	respondNoContent(context, err)
}

func (service *Service) RestoreMedia(context *gin.Context) {
	err := service.repository.RestoreMedia(context, context.Param("id"), currentUser(context).ID)
	respondNoContent(context, err)
}

func (service *Service) DeleteMedia(context *gin.Context) {
	user := currentUser(context)
	mediaID := context.Param("id")
	paths, err := service.repository.GetOwnedMediaPaths(context, mediaID, user.ID)
	if err != nil {
		respondNoContent(context, err)
		return
	}
	absolutePaths := make([]string, 0, 2)
	for _, relativePath := range []string{paths.Media, paths.Thumbnail} {
		if relativePath == "" {
			continue
		}
		path, err := service.mediaPath(relativePath)
		if err != nil {
			respondNoContent(context, err)
			return
		}
		absolutePaths = append(absolutePaths, path)
	}
	stagingDir := filepath.Join(service.mediaDir, ".deleting", uuid.NewString())
	staged := make([]stagedMediaFile, 0, len(absolutePaths))
	for index, originalPath := range absolutePaths {
		if err := os.MkdirAll(stagingDir, 0o750); err != nil {
			context.JSON(http.StatusBadGateway, gin.H{"error": "unable to delete media file"})
			return
		}
		stagedPath := filepath.Join(stagingDir, strconv.Itoa(index)+"-"+filepath.Base(originalPath))
		if err := os.Rename(originalPath, stagedPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			_ = restoreStagedMedia(staged)
			_ = os.RemoveAll(stagingDir)
			context.JSON(http.StatusBadGateway, gin.H{"error": "unable to delete media file"})
			return
		}
		staged = append(staged, stagedMediaFile{original: originalPath, staged: stagedPath})
	}
	if err := service.repository.DeleteMedia(context, mediaID, user.ID); err != nil {
		if restoreErr := restoreStagedMedia(staged); restoreErr != nil {
			_ = context.Error(restoreErr)
			context.JSON(http.StatusBadGateway, gin.H{"error": "unable to restore media file after failed deletion"})
			return
		}
		_ = os.RemoveAll(stagingDir)
		respondNoContent(context, err)
		return
	}
	if err := os.RemoveAll(stagingDir); err != nil {
		_ = context.Error(err)
	}
	context.Status(http.StatusNoContent)
}

type stagedMediaFile struct {
	original string
	staged   string
}

func restoreStagedMedia(files []stagedMediaFile) error {
	var restoreErr error
	for index := len(files) - 1; index >= 0; index-- {
		if err := os.Rename(files[index].staged, files[index].original); err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	return restoreErr
}

func (service *Service) mediaPath(relativePath string) (string, error) {
	path := filepath.Join(service.mediaDir, filepath.FromSlash(relativePath))
	cleanRelative, err := filepath.Rel(service.mediaDir, path)
	if err != nil || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanRelative) {
		return "", ErrForbidden
	}
	return path, nil
}

func (service *Service) GetStorage(context *gin.Context) {
	storage, err := service.repository.GetStorage(context, currentUser(context).ID)
	respond(context, storage, err)
}

func currentUser(context *gin.Context) auth.User {
	value, _ := context.Get(auth.UserContextKey)
	user, _ := value.(auth.User)
	return user
}

func decodeJSON(context *gin.Context, target any) error {
	decoder := json.NewDecoder(io.LimitReader(context.Request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func respond(context *gin.Context, value any, err error) {
	if err == nil {
		context.JSON(http.StatusOK, value)
		return
	}
	respondError(context, err)
}

func respondNoContent(context *gin.Context, err error) {
	if err == nil {
		context.Status(http.StatusNoContent)
		return
	}
	respondError(context, err)
}

func respondError(context *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		context.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, ErrForbidden):
		context.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		_ = context.Error(err)
		context.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
