package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type memoryRepository struct {
	account Account
}

func (repository *memoryRepository) SetupComplete(context.Context) (bool, error) {
	return repository.account.ID != "", nil
}

func (repository *memoryRepository) CreateInitialAdmin(_ context.Context, account Account) error {
	if repository.account.ID != "" {
		return ErrAlreadyConfigured
	}
	repository.account = account
	return nil
}

func TestCreateAdminUsesBootstrapCredentialsOnce(t *testing.T) {
	repository := &memoryRepository{}
	service, err := NewService(repository, "bootstrap", "bootstrap-password", true)
	if err != nil {
		t.Fatal(err)
	}
	err = service.CreateAdmin(context.Background(), "bootstrap", "bootstrap-password", "owner", "permanent-password", "permanent-password")
	if err != nil {
		t.Fatal(err)
	}
	if repository.account.Username != "owner" || bcrypt.CompareHashAndPassword([]byte(repository.account.PasswordHash), []byte("permanent-password")) != nil {
		t.Fatalf("unexpected initial account: %#v", repository.account)
	}
	err = service.CreateAdmin(context.Background(), "bootstrap", "bootstrap-password", "other", "another-password", "another-password")
	if !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("expected already configured, got %v", err)
	}
}

func TestCreateAdminRejectsInvalidBootstrap(t *testing.T) {
	service, err := NewService(&memoryRepository{}, "bootstrap", "bootstrap-password", true)
	if err != nil {
		t.Fatal(err)
	}
	err = service.CreateAdmin(context.Background(), "bootstrap", "wrong-password", "owner", "permanent-password", "permanent-password")
	if !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("expected invalid bootstrap, got %v", err)
	}
}

func TestSetupPageLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &memoryRepository{}
	service, err := NewService(repository, "bootstrap", "bootstrap-password", true)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	service.RegisterRoutes(router)

	status := httptest.NewRecorder()
	router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/setup/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"required":true`) {
		t.Fatalf("unexpected setup status: %d %q", status.Code, status.Body.String())
	}

	body := `{"bootstrapUsername":"bootstrap","bootstrapPassword":"bootstrap-password","username":"owner","password":"permanent-password","confirmPassword":"permanent-password"}`
	request := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("unexpected setup response: %d %q", response.Code, response.Body.String())
	}

	closed := httptest.NewRecorder()
	router.ServeHTTP(closed, httptest.NewRequest(http.MethodGet, "/setup/status", nil))
	if closed.Code != http.StatusOK || !strings.Contains(closed.Body.String(), `"required":false`) {
		t.Fatalf("expected setup to close, got %d %q", closed.Code, closed.Body.String())
	}

	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if page.Code != http.StatusSeeOther || page.Header().Get("Location") != "/" {
		t.Fatalf("expected setup route to open the web app, got %d %q", page.Code, page.Header().Get("Location"))
	}
}

func TestSetupPageDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, err := NewService(&memoryRepository{}, "bootstrap", "bootstrap-password", false)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	service.RegisterRoutes(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected disabled setup page to return 404, got %d", response.Code)
	}
}
