package batch

import (
	"context"
	"net/http"
	"strings"
	"time"

	"pal-next-gallery-server/app/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Batch struct {
	ID             string    `json:"id"`
	OwnerID        string    `json:"-"`
	Name           string    `json:"name"`
	ExpectedFiles  int       `json:"expectedFiles"`
	ExpectedBytes  int64     `json:"expectedBytes"`
	CompletedFiles int       `json:"completedFiles"`
	CompletedBytes int64     `json:"completedBytes"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
}

type Repository interface {
	CreateUploadBatch(context.Context, Batch) error
	GetUploadBatch(context.Context, string, string) (Batch, error)
	CancelUploadBatch(context.Context, string, string) error
}

type UploadCanceler interface {
	CancelBatchUploads(context.Context, string, string) error
}

type Service struct {
	repository Repository
	uploads    UploadCanceler
}

func New(repository Repository, uploads ...UploadCanceler) *Service {
	service := &Service{repository: repository}
	if len(uploads) > 0 {
		service.uploads = uploads[0]
	}
	return service
}

func (service *Service) Create(context *gin.Context) {
	user, ok := currentUser(context)
	var request struct {
		Name          string `json:"name"`
		ExpectedFiles int    `json:"expectedFiles"`
		ExpectedBytes int64  `json:"expectedBytes"`
	}
	if !ok || context.ShouldBindJSON(&request) != nil || request.ExpectedFiles < 1 || request.ExpectedBytes < 1 || request.ExpectedFiles > 10000 {
		context.JSON(http.StatusBadRequest, gin.H{"error": "valid name, expectedFiles, and expectedBytes are required"})
		return
	}
	batch := Batch{ID: uuid.NewString(), OwnerID: user.ID, Name: strings.TrimSpace(request.Name), ExpectedFiles: request.ExpectedFiles,
		ExpectedBytes: request.ExpectedBytes, Status: "active", CreatedAt: time.Now().UTC()}
	if err := service.repository.CreateUploadBatch(context, batch); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "unable to create upload batch"})
		return
	}
	context.JSON(http.StatusCreated, batch)
}

func (service *Service) Get(context *gin.Context) {
	user, _ := currentUser(context)
	batch, err := service.repository.GetUploadBatch(context, context.Param("id"), user.ID)
	if err != nil {
		context.JSON(http.StatusNotFound, gin.H{"error": "upload batch not found"})
		return
	}
	context.JSON(http.StatusOK, batch)
}

func (service *Service) Cancel(context *gin.Context) {
	user, _ := currentUser(context)
	if err := service.repository.CancelUploadBatch(context, context.Param("id"), user.ID); err != nil {
		context.JSON(http.StatusNotFound, gin.H{"error": "upload batch not found"})
		return
	}
	if service.uploads != nil {
		if err := service.uploads.CancelBatchUploads(context.Request.Context(), context.Param("id"), user.ID); err != nil {
			_ = context.Error(err)
			context.JSON(http.StatusInternalServerError, gin.H{"error": "unable to cancel unfinished uploads"})
			return
		}
	}
	context.Status(http.StatusNoContent)
}

func currentUser(context *gin.Context) (auth.User, bool) {
	value, exists := context.Get(auth.UserContextKey)
	user, ok := value.(auth.User)
	return user, exists && ok
}
