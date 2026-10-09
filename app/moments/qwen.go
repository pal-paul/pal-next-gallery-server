package moments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type QwenEnricher struct {
	endpoint   *url.URL
	model      string
	mediaDir   string
	httpClient *http.Client
}

func NewQwenEnricher(endpoint, model, mediaDir string) (*QwenEnricher, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_QWEN_URL must be an HTTP URL")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_QWEN_MODEL is required")
	}
	return &QwenEnricher{
		endpoint: parsed, model: strings.TrimSpace(model), mediaDir: filepath.Clean(mediaDir), httpClient: http.DefaultClient,
	}, nil
}

func (enricher *QwenEnricher) Enrich(ctx context.Context, moment Moment, candidates []Candidate) (Metadata, error) {
	images := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		imagePath, err := safeMediaPath(enricher.mediaDir, candidate.SourcePath)
		if err != nil {
			return Metadata{}, err
		}
		image, err := os.ReadFile(imagePath)
		if err != nil {
			return Metadata{}, fmt.Errorf("read representative image: %w", err)
		}
		images = append(images, base64.StdEncoding.EncodeToString(image))
	}
	prompt := fmt.Sprintf(`Decide whether these photos form one coherent personal moment from %s to %s. Return JSON only with title, description, and confidence. The description must only include details shared by the photos. Confidence must be a number from 0 to 1 representing the fraction of supplied photos that support the shared description. Do not invent people, places, dates, or activities that are not visible.`,
		moment.StartTime.Format("2006-01-02 15:04"), moment.EndTime.Format("2006-01-02 15:04"))
	payload := struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Format   string `json:"format"`
		Messages []struct {
			Role    string   `json:"role"`
			Content string   `json:"content"`
			Images  []string `json:"images,omitempty"`
		} `json:"messages"`
	}{Model: enricher.model, Stream: false, Format: "json"}
	payload.Messages = append(payload.Messages, struct {
		Role    string   `json:"role"`
		Content string   `json:"content"`
		Images  []string `json:"images,omitempty"`
	}{Role: "user", Content: prompt, Images: images})
	body, err := json.Marshal(payload)
	if err != nil {
		return Metadata{}, err
	}
	requestURL := *enricher.endpoint
	requestURL.Path = path.Join(requestURL.Path, "/api/chat")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return Metadata{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := enricher.httpClient.Do(request)
	if err != nil {
		return Metadata{}, fmt.Errorf("request Qwen metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Metadata{}, fmt.Errorf("Qwen metadata returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return Metadata{}, fmt.Errorf("decode Qwen response: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal([]byte(result.Message.Content), &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode Qwen metadata: %w", err)
	}
	metadata.Title = strings.TrimSpace(metadata.Title)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if metadata.Confidence > 1 && metadata.Confidence <= 100 {
		metadata.Confidence /= 100
	}
	if metadata.Title == "" || metadata.Confidence < 0 || metadata.Confidence > 1 {
		return Metadata{}, fmt.Errorf("Qwen returned invalid metadata")
	}
	return metadata, nil
}
