package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

const sessionCookie = "pal_medias_uploader_session"
const UsernameContextKey = "auth.username"
const UserContextKey = "auth.user"

func (handler *Service) Login(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if !handler.allowLogin(input.Username, handler.clientAddress(request)) {
		writeError(writer, http.StatusTooManyRequests, errors.New("too many authentication attempts"))
		return
	}
	challenge, err := handler.BeginLogin(request.Context(), input.Username, input.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		handler.recordFailedAttempt(handler.loginAttempts,
			"account:"+strings.ToLower(strings.TrimSpace(input.Username)), "ip:"+handler.clientAddress(request))
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err == nil && challenge.Authenticated {
		http.SetCookie(writer, handler.authCookie(request, challenge.SessionToken, 30*24*60*60))
	}
	respond(writer, challenge, err)
}

func (handler *Service) VerifyTOTP(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		ChallengeToken string `json:"challengeToken"`
		Code           string `json:"code"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if !handler.allowVerify(input.ChallengeToken, handler.clientAddress(request)) {
		writeError(writer, http.StatusTooManyRequests, errors.New("too many authentication attempts"))
		return
	}
	token, err := handler.Verify(request.Context(), input.ChallengeToken, input.Code)
	if errors.Is(err, ErrInvalidChallenge) || errors.Is(err, ErrInvalidCode) {
		handler.recordFailedAttempt(handler.verifyAttempts,
			"token:"+tokenHash(input.ChallengeToken), "ip:"+handler.clientAddress(request))
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(writer, handler.authCookie(request, token, 30*24*60*60))
	writeJSON(writer, http.StatusOK, map[string]string{"status": "authenticated"})
}

func (handler *Service) VerifyRecoveryCodeHandler(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		ChallengeToken string `json:"challengeToken"`
		Code           string `json:"code"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if !handler.allowVerify(input.ChallengeToken, handler.clientAddress(request)) {
		writeError(writer, http.StatusTooManyRequests, errors.New("too many authentication attempts"))
		return
	}
	token, err := handler.VerifyRecoveryCode(request.Context(), input.ChallengeToken, input.Code)
	if errors.Is(err, ErrInvalidChallenge) || errors.Is(err, ErrInvalidCode) {
		handler.recordFailedAttempt(handler.verifyAttempts,
			"token:"+tokenHash(input.ChallengeToken), "ip:"+handler.clientAddress(request))
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(writer, handler.authCookie(request, token, 30*24*60*60))
	writeJSON(writer, http.StatusOK, map[string]string{"status": "authenticated"})
}

func (handler *Service) RecoveryCodes(context *gin.Context) {
	user, ok := context.Get(UserContextKey)
	if !ok {
		writeError(context.Writer, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	authenticatedUser, ok := user.(User)
	if !ok {
		writeError(context.Writer, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	codes, err := handler.GenerateRecoveryCodes(context.Request.Context(), authenticatedUser.ID)
	if err != nil {
		writeError(context.Writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(context.Writer, http.StatusCreated, map[string]any{"recoveryCodes": codes})
}

func (handler *Service) Session(writer http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	user, err := handler.Authenticate(request.Context(), cookie.Value)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"id": user.ID, "username": user.Username, "role": user.Role,
		"uploadFolder": user.UploadFolder, "totpEnabled": user.TOTPSecret != "",
	})
}

func (handler *Service) LogoutHandler(writer http.ResponseWriter, request *http.Request) {
	cookie, _ := request.Cookie(sessionCookie)
	if cookie != nil {
		_ = handler.Logout(request.Context(), cookie.Value)
	}
	http.SetCookie(writer, handler.authCookie(request, "", -1))
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Service) authCookie(request *http.Request, value string, maxAge int) *http.Cookie {
	origin := request.Header.Get("Origin")
	secure := request.TLS != nil || (handler.isTrustedProxy(request) && strings.EqualFold(firstHeaderValue(request.Header.Get("X-Forwarded-Proto")), "https"))
	sameSite := http.SameSiteStrictMode
	if secure && origin != "" {
		sameSite = http.SameSiteNoneMode
	}
	// #nosec G124 -- Secure is derived from direct TLS or a verified trusted proxy.
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: secure, SameSite: sameSite, MaxAge: maxAge}
}

func (handler *Service) RequireAuth() gin.HandlerFunc {
	return func(context *gin.Context) {
		cookie, err := context.Request.Cookie(sessionCookie)
		if err != nil {
			writeError(context.Writer, http.StatusUnauthorized, ErrUnauthorized)
			context.Abort()
			return
		}
		user, err := handler.Authenticate(context.Request.Context(), cookie.Value)
		if err != nil {
			writeError(context.Writer, http.StatusUnauthorized, ErrUnauthorized)
			context.Abort()
			return
		}
		context.Set(UsernameContextKey, user.Username)
		context.Set(UserContextKey, user)
		context.Next()
	}
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (handler *Service) clientAddress(request *http.Request) string {
	if handler.isTrustedProxy(request) {
		for _, value := range []string{firstHeaderValue(request.Header.Get("X-Forwarded-For")), request.Header.Get("X-Real-IP")} {
			if address, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
				return address.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func (handler *Service) isTrustedProxy(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, prefix := range handler.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func firstHeaderValue(value string) string {
	if first, _, found := strings.Cut(value, ","); found {
		return strings.TrimSpace(first)
	}
	return strings.TrimSpace(value)
}

func respond(writer http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	slog.Error("request failed", "status", status, "error", err)
	writeJSON(writer, status, map[string]string{"error": http.StatusText(status)})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Error("encode response", "error", err)
	}
}
