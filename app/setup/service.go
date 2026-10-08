package setup

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrAlreadyConfigured = errors.New("initial administrator is already configured")
	ErrInvalidBootstrap  = errors.New("invalid bootstrap credentials")
	ErrInvalidAdmin      = errors.New("admin username and a password of at least 12 characters are required")
	ErrPasswordMismatch  = errors.New("admin passwords do not match")
)

type Account struct {
	ID           string
	Username     string
	PasswordHash string
}

type Repository interface {
	SetupComplete(context.Context) (bool, error)
	CreateInitialAdmin(context.Context, Account) error
}

type Service struct {
	repository        Repository
	bootstrapUsername string
	bootstrapPassword string
	frontendEnabled   bool
}

func NewService(repository Repository, bootstrapUsername, bootstrapPassword string, frontendEnabled bool) (*Service, error) {
	bootstrapUsername = strings.TrimSpace(bootstrapUsername)
	if bootstrapUsername == "" || len(bootstrapPassword) < 12 {
		return nil, errors.New("bootstrap username and a password of at least 12 characters are required")
	}
	return &Service{
		repository: repository, bootstrapUsername: bootstrapUsername,
		bootstrapPassword: bootstrapPassword, frontendEnabled: frontendEnabled,
	}, nil
}

func (service *Service) CreateAdmin(ctx context.Context, bootstrapUsername, bootstrapPassword, username, password, confirmation string) error {
	configured, err := service.repository.SetupComplete(ctx)
	if err != nil {
		return err
	}
	if configured {
		return ErrAlreadyConfigured
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(bootstrapUsername)), []byte(service.bootstrapUsername)) != 1 ||
		subtle.ConstantTimeCompare([]byte(bootstrapPassword), []byte(service.bootstrapPassword)) != 1 {
		return ErrInvalidBootstrap
	}
	username = strings.TrimSpace(username)
	if username == "" || len(password) < 12 {
		return ErrInvalidAdmin
	}
	if password != confirmation {
		return ErrPasswordMismatch
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	return service.repository.CreateInitialAdmin(ctx, Account{
		ID: uuid.NewString(), Username: username, PasswordHash: string(hash),
	})
}

func (service *Service) RegisterRoutes(router gin.IRoutes) {
	router.GET("/setup", service.SetupPage)
	router.GET("/setup/status", service.SetupStatus)
	router.POST("/setup", service.SubmitSetup)
}

func (service *Service) SetupPage(context *gin.Context) {
	if !service.frontendEnabled {
		context.Status(http.StatusNotFound)
		return
	}
	context.Redirect(http.StatusSeeOther, "/")
}

func (service *Service) SetupStatus(context *gin.Context) {
	if !service.frontendEnabled {
		context.Status(http.StatusNotFound)
		return
	}
	configured, err := service.repository.SetupComplete(context.Request.Context())
	if err != nil {
		_ = context.Error(err)
		context.Status(http.StatusInternalServerError)
		return
	}
	context.JSON(http.StatusOK, gin.H{"required": !configured})
}

func (service *Service) SubmitSetup(context *gin.Context) {
	if !service.pageAvailable(context) {
		return
	}
	context.Request.Body = http.MaxBytesReader(context.Writer, context.Request.Body, 64<<10)
	var request struct {
		BootstrapUsername string `json:"bootstrapUsername"`
		BootstrapPassword string `json:"bootstrapPassword"`
		Username          string `json:"username"`
		Password          string `json:"password"`
		ConfirmPassword   string `json:"confirmPassword"`
	}
	if err := context.ShouldBindJSON(&request); err != nil {
		context.JSON(http.StatusBadRequest, gin.H{"error": "invalid setup request"})
		return
	}
	err := service.CreateAdmin(
		context.Request.Context(),
		request.BootstrapUsername, request.BootstrapPassword,
		request.Username, request.Password, request.ConfirmPassword,
	)
	if err != nil {
		if errors.Is(err, ErrAlreadyConfigured) {
			context.Status(http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrInvalidBootstrap) {
			context.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, ErrInvalidAdmin) || errors.Is(err, ErrPasswordMismatch) {
			context.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		_ = context.Error(err)
		context.JSON(http.StatusInternalServerError, gin.H{"error": "unable to complete setup"})
		return
	}
	context.Status(http.StatusNoContent)
}

func (service *Service) pageAvailable(context *gin.Context) bool {
	if !service.frontendEnabled {
		context.Status(http.StatusNotFound)
		return false
	}
	configured, err := service.repository.SetupComplete(context.Request.Context())
	if err != nil {
		_ = context.Error(err)
		context.Status(http.StatusInternalServerError)
		return false
	}
	if configured {
		context.Status(http.StatusNotFound)
		return false
	}
	return true
}
