package moments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQwenEnricherSendsRepresentativeImageAndParsesMetadata(t *testing.T) {
	mediaDir := t.TempDir()
	imageBytes := []byte("representative image")
	if err := os.WriteFile(filepath.Join(mediaDir, "photo.jpg"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "qwen3-vl:4b" || len(payload.Messages) != 1 || len(payload.Messages[0].Images) != 1 {
			t.Errorf("unexpected payload: %#v", payload)
		}
		if payload.Messages[0].Images[0] != base64.StdEncoding.EncodeToString(imageBytes) {
			t.Error("unexpected image payload")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"message":{"content":"{\"title\":\"Seaside walk\",\"description\":\"A bright day near the water.\",\"confidence\":86}"}}`))
	}))
	defer server.Close()

	enricher, err := NewQwenEnricher(server.URL, "qwen3-vl:4b", mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := enricher.Enrich(context.Background(), Moment{
		StartTime: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC),
	}, []Candidate{{ID: "media-1", SourcePath: "photo.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Title != "Seaside walk" || metadata.Description != "A bright day near the water." || metadata.Confidence != 0.86 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}
