package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestLoggerIncludesHandlerError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	router := gin.New()
	router.Use(requestLoggerMiddleware())
	router.GET("/albums", func(context *gin.Context) {
		_ = context.Error(errors.New("database unavailable"))
		context.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/albums", nil))

	if !strings.Contains(output.String(), "database unavailable") {
		t.Fatalf("request log does not contain handler error: %s", output.String())
	}
}

func TestCORSMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		origin         string
		fetchSite      string
		allowedOrigins string
		wantStatus     int
	}{
		{name: "same origin", origin: "http://127.0.0.1:8081", wantStatus: http.StatusNoContent},
		{name: "null same origin navigation", origin: "null", fetchSite: "same-origin", wantStatus: http.StatusNoContent},
		{name: "null cross site navigation", origin: "null", fetchSite: "cross-site", wantStatus: http.StatusForbidden},
		{name: "explicitly allowed", origin: "http://localhost:3000", allowedOrigins: "http://localhost:3000", wantStatus: http.StatusNoContent},
		{name: "foreign origin", origin: "https://example.com", wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(corsMiddleware(test.allowedOrigins))
			router.GET("/asset", func(context *gin.Context) {
				context.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/asset", nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}
