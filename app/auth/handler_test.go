package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
)

func TestLoginRateLimit(t *testing.T) {
	service := NewService(&memoryRepository{}, "PAL Gallery Test")
	for attempt := 1; attempt <= loginAccountAttempts+1; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		service.Login(response, request)
		want := http.StatusUnauthorized
		if attempt > loginAccountAttempts {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d: got status %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestSessionRejectsMissingCookie(t *testing.T) {
	service := NewService(&memoryRepository{}, "PAL Gallery Test")
	request := httptest.NewRequest(http.MethodGet, "/session", nil)
	response := httptest.NewRecorder()

	service.Session(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestVerifyTOTPReturnsActionableUnauthorizedReason(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		expectedCode  string
		expectedError string
	}{
		{
			name:          "invalid challenge",
			body:          `{"challengeToken":"missing","code":"123456"}`,
			expectedCode:  "invalid_challenge",
			expectedError: "Authentication challenge expired. Sign in again.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(&memoryRepository{}, "PAL Gallery Test")
			request := httptest.NewRequest(http.MethodPost, "/auth/verify", bytes.NewBufferString(test.body))
			response := httptest.NewRecorder()

			service.VerifyTOTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
			}
			var payload map[string]string
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["code"] != test.expectedCode || payload["error"] != test.expectedError {
				t.Fatalf("got response %#v", payload)
			}
		})
	}
}

func TestLoginAndVerifyTOTPHandlersCreateSession(t *testing.T) {
	repository := &memoryRepository{user: passwordUser(t, true)}
	service := NewService(repository, "PAL Gallery Test")
	loginRequest := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(
		`{"username":"admin","password":"correct-horse-battery"}`,
	))
	loginResponse := httptest.NewRecorder()
	service.Login(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login returned status %d", loginResponse.Code)
	}
	var challenge LoginChallenge
	if err := json.NewDecoder(loginResponse.Body).Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(challenge.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	verifyBody, err := json.Marshal(map[string]string{"challengeToken": challenge.ChallengeToken, "code": code})
	if err != nil {
		t.Fatal(err)
	}
	verifyRequest := httptest.NewRequest(http.MethodPost, "/auth/verify", bytes.NewReader(verifyBody))
	verifyResponse := httptest.NewRecorder()

	service.VerifyTOTP(verifyResponse, verifyRequest)

	if verifyResponse.Code != http.StatusOK {
		t.Fatalf("verify returned status %d: %s", verifyResponse.Code, verifyResponse.Body.String())
	}
	if len(verifyResponse.Result().Cookies()) != 1 || verifyResponse.Result().Cookies()[0].Name != sessionCookie {
		t.Fatalf("verify did not issue %s cookie", sessionCookie)
	}
}

func TestRequireAuthRejectsMissingCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := NewService(&memoryRepository{}, "PAL Gallery Test")
	router := gin.New()
	router.GET("/protected", service.RequireAuth(), func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestForwardedHeadersRequireTrustedProxy(t *testing.T) {
	trusted := NewService(&memoryRepository{}, "PAL Gallery Test", WithTrustedProxies([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "192.0.2.10, 10.0.0.1")
	request.Header.Set("X-Forwarded-Proto", "https")
	if address := trusted.clientAddress(request); address != "192.0.2.10" {
		t.Fatalf("got client address %q", address)
	}
	if cookie := trusted.authCookie(request, "token", 60); !cookie.Secure {
		t.Fatal("trusted HTTPS proxy did not produce a secure cookie")
	}

	untrusted := NewService(&memoryRepository{}, "PAL Gallery Test")
	if address := untrusted.clientAddress(request); address != "10.0.0.2" {
		t.Fatalf("untrusted proxy changed client address to %q", address)
	}
	if cookie := untrusted.authCookie(request, "token", 60); cookie.Secure {
		t.Fatal("untrusted proxy marked cookie secure")
	}
}
