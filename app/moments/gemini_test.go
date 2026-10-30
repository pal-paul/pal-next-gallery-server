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

func TestNewGeminiPipelineUsesBoundedHTTPClient(t *testing.T) {
	pipeline, err := NewGeminiPipeline("https://example.com", "key", "vision", "text", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.httpClient.Timeout != geminiRequestTimeout {
		t.Fatalf("HTTP timeout = %s, want %s", pipeline.httpClient.Timeout, geminiRequestTimeout)
	}
}

func TestGeminiPipelineSeparatesVisionDescriptionFromTextSynthesis(t *testing.T) {
	mediaDir := t.TempDir()
	imageBytes := []byte("representative image")
	if err := os.WriteFile(filepath.Join(mediaDir, "photo.jpg"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("missing Gemini API key")
		}
		var payload struct {
			Contents []struct {
				Parts []struct {
					Text       string       `json:"text"`
					InlineData *geminiImage `json:"inline_data"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				ResponseMIMEType   string         `json:"responseMimeType"`
				ResponseJSONSchema map[string]any `json:"responseJsonSchema"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.GenerationConfig.ResponseMIMEType != "application/json" {
			t.Errorf("response MIME type = %q", payload.GenerationConfig.ResponseMIMEType)
		}
		if payload.GenerationConfig.ResponseJSONSchema["type"] != "object" {
			t.Errorf("unexpected response schema: %#v", payload.GenerationConfig.ResponseJSONSchema)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/models/gemini-vision:generateContent":
			image := payload.Contents[0].Parts[1].InlineData
			if image == nil || image.MIMEType != "image/jpeg" || image.Data != base64.StdEncoding.EncodeToString(imageBytes) {
				t.Errorf("unexpected image payload: %#v", image)
			}
			_, _ = writer.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"people\":[\"two people\"],\"activities\":[\"walking\"],\"location_type\":\"beach\",\"objects\":[],\"scene\":\"shoreline\",\"weather\":\"sunny\",\"description\":\"Two people walking by the sea.\"}"}]}}]}`))
		case "/models/gemini-text:generateContent":
			if len(payload.Contents[0].Parts) != 1 || !strings.Contains(payload.Contents[0].Parts[0].Text, "Two people walking by the sea.") {
				t.Errorf("unexpected synthesis payload: %#v", payload.Contents[0].Parts)
			}
			_, _ = writer.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"title\":\"Seaside walk\",\"description\":\"A sunny walk by the sea.\",\"confidence\":0.9}"}]}}]}`))
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	pipeline, err := NewGeminiPipeline(server.URL, "test-key", "gemini-vision", "gemini-text", mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.httpClient = server.Client()
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
	if requests != 2 || description.Model != "gemini-vision" || metadata.Title != "Seaside walk" || metadata.Confidence != 0.9 {
		t.Fatalf("unexpected pipeline result: requests=%d description=%#v metadata=%#v", requests, description, metadata)
	}
}

func TestGeminiPipelineRequiresAPIKey(t *testing.T) {
	_, err := NewGeminiPipeline("", "", "gemini-3.5-flash-lite", "gemini-3.5-flash-lite", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeminiEmbedsDescriptorTextOnly(t *testing.T) {
	for _, scenario := range []string{"success", "zero vector", "wrong dimensions", "HTTP failure"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/models/gemini-embedding-001:embedContent" || request.Header.Get("x-goog-api-key") != "key" {
					t.Errorf("unexpected embedding request: %s", request.URL.Path)
				}
				var payload struct {
					Model                string `json:"model"`
					TaskType             string `json:"taskType"`
					OutputDimensionality int    `json:"outputDimensionality"`
					Content              struct {
						Parts []map[string]string `json:"parts"`
					} `json:"content"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Model != "models/gemini-embedding-001" || payload.TaskType != "SEMANTIC_SIMILARITY" || payload.OutputDimensionality != 768 ||
					len(payload.Content.Parts) != 1 || payload.Content.Parts[0]["text"] != "Ocean outing beach boats sailing" || len(payload.Content.Parts[0]) != 1 {
					t.Errorf("not a descriptor-only embedding request: %+v", payload)
				}
				if scenario == "HTTP failure" {
					writer.WriteHeader(http.StatusTooManyRequests)
					return
				}
				values := make([]float64, 768)
				if scenario != "zero vector" {
					values[0] = 1
				}
				if scenario == "wrong dimensions" {
					values = values[:2]
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"embedding": map[string]any{"values": values}})
			}))
			defer server.Close()
			pipeline, err := NewGeminiPipeline(server.URL, "key", "vision", "text", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			pipeline.httpClient = server.Client()
			embedding, err := pipeline.EmbedDescription(context.Background(), ImageDescription{Description: "Ocean outing", Keywords: []string{"beach"}, Objects: []string{"boats"}, Activities: []string{"sailing"}})
			if scenario == "success" {
				if err != nil || len(embedding) != 768 || pipeline.TextEmbeddingModel() != "gemini-embedding-001:SEMANTIC_SIMILARITY:768" {
					t.Fatalf("embedding = %v, %v", embedding, err)
				}
			} else if err == nil {
				t.Fatal("invalid embedding response succeeded")
			}
		})
	}
}

func TestClusterMergePromptTreatsVisualClustersAsEventFragments(t *testing.T) {
	prompt := clusterMergePrompt(`[[{"scene":"atrium"}],[{"scene":"exhibit"}],[{"scene":"lantern detail"}]]`)

	for _, instruction := range []string{
		"First partition clusters by incompatible event context",
		"Never merge an outdoor cluster with an indoor cluster",
		"visual clusters, not event boundaries",
		"atrium, storefronts, cultural exhibits, statues, portraits, decorative displays, and close-up detail photos",
		"Attach singleton decor or detail clusters",
	} {
		if !strings.Contains(prompt, instruction) {
			t.Errorf("cluster merge prompt is missing %q: %s", instruction, prompt)
		}
	}
}
