package moments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestQwenPipelineSeparatesVisionDescriptionFromTextSynthesis(t *testing.T) {
	mediaDir := t.TempDir()
	imageBytes := []byte("representative image")
	if err := os.WriteFile(filepath.Join(mediaDir, "photo.jpg"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string   `json:"content"`
				Images  []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch payload.Model {
		case "qwen3-vl:4b":
			if len(payload.Messages[0].Images) != 1 {
				t.Errorf("vision request has %d images", len(payload.Messages[0].Images))
			}
			_, _ = writer.Write([]byte(`{"message":{"content":"{\"people\":[\"two people\"],\"activities\":[\"walking\"],\"location_type\":\"beach\",\"objects\":[],\"scene\":\"shoreline\",\"weather\":\"sunny\",\"description\":\"Two people walking by the sea.\"}"}}`))
		case "qwen3:4b":
			if len(payload.Messages[0].Images) != 0 {
				t.Errorf("text synthesis request unexpectedly contains images")
			}
			if !strings.Contains(payload.Messages[0].Content, "Two people walking by the sea.") {
				t.Errorf("text synthesis request lacks structured description: %q", payload.Messages[0].Content)
			}
			_, _ = writer.Write([]byte(`{"message":{"content":"{\"title\":\"Seaside walk\",\"description\":\"A sunny walk by the sea.\",\"confidence\":0.9}"}}`))
		default:
			t.Errorf("unexpected model %q", payload.Model)
		}
	}))
	defer server.Close()

	pipeline, err := NewQwenPipeline(server.URL, "qwen3-vl:4b", "qwen3:4b", mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	description, err := pipeline.Describe(context.Background(), Candidate{ID: "media-1", SourcePath: "photo.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := pipeline.Synthesize(context.Background(), Moment{
		StartTime: time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC),
	}, []ImageDescription{description})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || description.Model != "qwen3-vl:4b" || metadata.Title != "Seaside walk" || metadata.Confidence != 0.9 {
		t.Fatalf("unexpected pipeline result: requests=%d description=%#v metadata=%#v", requests, description, metadata)
	}
}
