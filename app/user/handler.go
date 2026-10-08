package user

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp"
)

func (service *Service) RequireRole(role string) gin.HandlerFunc {
	return func(context *gin.Context) {
		identity, ok := currentUser(context)
		if !ok || identity.Role != role {
			context.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		context.Next()
	}
}

func (service *Service) ConfigurationPage(context *gin.Context) {
	context.Header("Content-Type", "text/html; charset=utf-8")
	if err := configurationTemplate.ExecuteTemplate(context.Writer, "config.html", nil); err != nil {
		internalError(context, err)
	}
}

func (service *Service) MediaPage(context *gin.Context) {
	context.Header("Content-Type", "text/html; charset=utf-8")
	if err := configurationTemplate.ExecuteTemplate(context.Writer, "media.html", nil); err != nil {
		internalError(context, err)
	}
}

func (service *Service) ListUsers(context *gin.Context) {
	accounts, err := service.repository.ListUsers(context.Request.Context())
	if err != nil {
		internalError(context, err)
		return
	}
	context.JSON(http.StatusOK, accounts)
}

func (service *Service) StorageDashboard(context *gin.Context) {
	accounts, err := service.repository.ListUsers(context.Request.Context())
	if err != nil {
		internalError(context, err)
		return
	}
	var total int64
	for _, account := range accounts {
		total += account.StorageUsed
	}
	context.JSON(http.StatusOK, gin.H{"totalUsedBytes": total, "users": accounts})
}

func (service *Service) SetUserQuota(context *gin.Context) {
	identity, _ := currentUser(context)
	var request struct {
		StorageQuota *int64 `json:"storageQuotaBytes"`
	}
	if err := decodeJSON(context, &request); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid quota request"})
		return
	}
	if err := service.SetQuota(context.Request.Context(), identity.Role, context.Param("id"), request.StorageQuota); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	context.Status(http.StatusNoContent)
}

func (service *Service) SetUserTrashRetention(context *gin.Context) {
	identity, _ := currentUser(context)
	var request struct {
		Days int `json:"days"`
	}
	if err := decodeJSON(context, &request); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid trash retention request"})
		return
	}
	if err := service.SetTrashRetention(context.Request.Context(), identity.Role, context.Param("id"), request.Days); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	context.Status(http.StatusNoContent)
}

func (service *Service) CreateUser(context *gin.Context) {
	identity, _ := currentUser(context)
	var request struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		Role         string `json:"role"`
		UploadFolder string `json:"uploadFolder"`
	}
	if err := decodeJSON(context, &request); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid user request"})
		return
	}
	account, provisioningURI, err := service.CreateAccount(context.Request.Context(), identity.Role, request.Username, request.Password, request.Role, request.UploadFolder)
	if err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	qrCode, err := qrDataURI(provisioningURI)
	if err != nil {
		internalError(context, err)
		return
	}
	context.JSON(http.StatusCreated, gin.H{"user": account, "provisioningUri": provisioningURI, "qrCode": qrCode})
}

func (service *Service) ResetUserPassword(context *gin.Context) {
	identity, _ := currentUser(context)
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(context, &request); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid password reset request"})
		return
	}
	if err := service.ResetPassword(context.Request.Context(), identity.Role, context.Param("id"), request.Password); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	context.Status(http.StatusNoContent)
}

func (service *Service) SetMyFolder(context *gin.Context) {
	identity, _ := currentUser(context)
	var request struct {
		UploadFolder string `json:"uploadFolder"`
	}
	if err := decodeJSON(context, &request); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder request"})
		return
	}
	if err := service.SetUploadFolder(context.Request.Context(), identity.ID, identity.Role, request.UploadFolder); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	context.Status(http.StatusNoContent)
}

func (service *Service) ListMedia(context *gin.Context) {
	identity, _ := currentUser(context)
	media, err := service.repository.ListAccessibleMedia(context.Request.Context(), identity.ID)
	if err != nil {
		internalError(context, err)
		return
	}
	context.JSON(http.StatusOK, media)
}

func (service *Service) ShareMedia(context *gin.Context) {
	service.changeShare(context, false)
}

func (service *Service) UnshareMedia(context *gin.Context) {
	service.changeShare(context, true)
}

func (service *Service) changeShare(context *gin.Context, remove bool) {
	identity, _ := currentUser(context)
	username := ""
	if remove {
		username = strings.TrimSpace(context.Query("username"))
	}
	var request struct {
		Username   string `json:"username"`
		Permission string `json:"permission"`
	}
	if username == "" {
		if err := decodeJSON(context, &request); err != nil || strings.TrimSpace(request.Username) == "" {
			context.JSON(http.StatusBadRequest, gin.H{"error": "username is required"})
			return
		}
		username = strings.TrimSpace(request.Username)
	}
	var err error
	if remove {
		err = service.repository.UnshareMedia(context.Request.Context(), context.Param("id"), identity.ID, username)
	} else {
		permission := strings.ToLower(strings.TrimSpace(request.Permission))
		if !validSharePermission(permission) {
			context.JSON(http.StatusBadRequest, gin.H{"error": "permission must be read or write"})
			return
		}
		err = service.repository.ShareMedia(context.Request.Context(), context.Param("id"), identity.ID, username, permission)
	}
	if err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "unable to update share"})
		return
	}
	context.Status(http.StatusNoContent)
}

func (service *Service) DownloadMedia(context *gin.Context) {
	identity, _ := currentUser(context)
	media, err := service.repository.GetAccessibleMedia(context.Request.Context(), context.Param("id"), identity.ID)
	if err != nil {
		context.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}
	path, err := service.mediaPath(media.MediaPath)
	if err != nil {
		internalError(context, err)
		return
	}
	context.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": media.Filename}))
	context.File(path)
}

func (service *Service) DownloadThumbnail(context *gin.Context) {
	identity, _ := currentUser(context)
	path, err := service.repository.GetAccessibleThumbnailPath(context.Request.Context(), context.Param("id"), identity.ID)
	if err != nil {
		context.JSON(http.StatusNotFound, gin.H{"error": "thumbnail not found"})
		return
	}
	resolved, err := service.mediaPath(path)
	if err != nil {
		context.JSON(http.StatusNotFound, gin.H{"error": "thumbnail not found"})
		return
	}
	context.Header("Cache-Control", "private, max-age=86400")
	context.File(resolved)
}

func (service *Service) mediaPath(relative string) (string, error) {
	path := filepath.Join(service.mediaDir, filepath.FromSlash(relative))
	relativePath, err := filepath.Rel(service.mediaDir, path)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return "", errors.New("invalid media path")
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func currentUser(context *gin.Context) (auth.User, bool) {
	value, exists := context.Get(auth.UserContextKey)
	identity, ok := value.(auth.User)
	return identity, exists && ok
}

func decodeJSON(context *gin.Context, target any) error {
	decoder := json.NewDecoder(io.LimitReader(context.Request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func qrDataURI(provisioningURI string) (string, error) {
	key, err := otp.NewKeyFromURL(provisioningURI)
	if err != nil {
		return "", err
	}
	image, err := key.Image(256, 256)
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	if err := png.Encode(&output, image); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(output.Bytes()), nil
}

func internalError(context *gin.Context, err error) {
	_ = context.Error(err)
	context.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}
