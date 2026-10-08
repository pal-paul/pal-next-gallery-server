package user

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

var ErrForbidden = errors.New("forbidden")
var ErrInvalidFolder = errors.New("upload folder must be a relative path without parent traversal")

//go:embed templates/*.html
var configurationTemplates embed.FS

var configurationTemplate = template.Must(template.ParseFS(configurationTemplates, "templates/*.html"))

type Repository interface {
	CreateUser(context.Context, NewAccount) error
	ListUsers(context.Context) ([]Account, error)
	UpdateUserFolder(context.Context, string, string) error
	ResetUserPassword(context.Context, string, string) error
	UpdateUserQuota(context.Context, string, *int64) error
	UpdateTrashRetention(context.Context, string, int) error
	ListAccessibleMedia(context.Context, string) ([]Media, error)
	GetAccessibleMedia(context.Context, string, string) (Media, error)
	GetAccessibleThumbnailPath(context.Context, string, string) (string, error)
	ShareMedia(context.Context, string, string, string, string) error
	UnshareMedia(context.Context, string, string, string) error
}

func validSharePermission(permission string) bool {
	return permission == "read" || permission == "write"
}

func (service *Service) SetTrashRetention(ctx context.Context, actorRole, userID string, days int) error {
	if actorRole != "admin" {
		return ErrForbidden
	}
	if days < 1 || days > 3650 {
		return errors.New("trash retention must be between 1 and 3650 days")
	}
	return service.repository.UpdateTrashRetention(ctx, userID, days)
}

func (service *Service) SetQuota(ctx context.Context, actorRole, userID string, quota *int64) error {
	if actorRole != "admin" {
		return ErrForbidden
	}
	if quota != nil && *quota < 1 {
		return errors.New("storage quota must be positive or null")
	}
	return service.repository.UpdateUserQuota(ctx, userID, quota)
}

func (service *Service) ResetPassword(ctx context.Context, actorRole, userID, password string) error {
	if actorRole != "admin" {
		return ErrForbidden
	}
	if len(password) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return service.repository.ResetUserPassword(ctx, userID, string(hash))
}

type Service struct {
	repository Repository
	issuer     string
	mediaDir   string
}

func NewService(repository Repository, issuer, mediaDir string) *Service {
	return &Service{repository: repository, issuer: issuer, mediaDir: filepath.Clean(mediaDir)}
}

func (service *Service) CreateAccount(ctx context.Context, actorRole, username, password, role, folder string) (Account, string, error) {
	if actorRole != "admin" {
		return Account{}, "", ErrForbidden
	}
	username = strings.TrimSpace(username)
	role = strings.ToLower(strings.TrimSpace(role))
	if username == "" || len(password) < 12 {
		return Account{}, "", errors.New("username and a password of at least 12 characters are required")
	}
	if role != "admin" && role != "user" {
		return Account{}, "", errors.New("role must be admin or user")
	}
	folder, err := sanitizeFolder(folder)
	if err != nil {
		return Account{}, "", err
	}
	if role == "user" && folder == "" {
		return Account{}, "", errors.New("an upload folder is required for regular users")
	}
	if role == "admin" {
		folder = ""
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Account{}, "", fmt.Errorf("hash password: %w", err)
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: service.issuer, AccountName: username})
	if err != nil {
		return Account{}, "", fmt.Errorf("generate TOTP key: %w", err)
	}
	account := Account{ID: uuid.NewString(), Username: username, Role: role, UploadFolder: folder, TOTPEnabled: true}
	err = service.repository.CreateUser(ctx, NewAccount{
		ID: account.ID, Username: username, PasswordHash: string(hash), Role: role,
		UploadFolder: folder, TOTPSecret: key.Secret(),
	})
	if err != nil {
		return Account{}, "", err
	}
	return account, key.URL(), nil
}

func (service *Service) SetUploadFolder(ctx context.Context, userID, role, folder string) error {
	if role != "user" {
		return ErrForbidden
	}
	folder, err := sanitizeFolder(folder)
	if err != nil {
		return err
	}
	if folder == "" {
		return errors.New("upload folder is required")
	}
	return service.repository.UpdateUserFolder(ctx, userID, folder)
}

func sanitizeFolder(folder string) (string, error) {
	folder = filepath.ToSlash(filepath.Clean(strings.TrimSpace(folder)))
	if folder == "." || folder == "" {
		return "", nil
	}
	if filepath.IsAbs(folder) || folder == ".." || strings.HasPrefix(folder, "../") {
		return "", ErrInvalidFolder
	}
	return folder, nil
}
