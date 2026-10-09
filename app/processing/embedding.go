package processing

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
	"strings"
	"time"
)

const embeddingRequestTimeout = 30 * time.Second

type Embedding struct {
	Model   string
	Version string
	Vector  []float64
}

type ImageEmbedder interface {
	Embed(context.Context, string) (Embedding, error)
}

type HTTPImageEmbedder struct {
	endpoint   *url.URL
	model      string
	httpClient *http.Client
}

func NewHTTPImageEmbedder(endpoint, model string) (*HTTPImageEmbedder, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_EMBEDDING_URL must be an HTTP URL")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_EMBEDDING_MODEL is required")
	}
	return &HTTPImageEmbedder{
		endpoint: parsed, model: strings.TrimSpace(model),
		httpClient: &http.Client{Timeout: embeddingRequestTimeout},
	}, nil
}

func (embedder *HTTPImageEmbedder) Embed(ctx context.Context, imagePath string) (Embedding, error) {
	image, err := os.ReadFile(imagePath)
	if err != nil {
		return Embedding{}, fmt.Errorf("read image: %w", err)
	}
	payload, err := json.Marshal(struct {
		Model string `json:"model"`
		Image string `json:"image"`
	}{Model: embedder.model, Image: base64.StdEncoding.EncodeToString(image)})
	if err != nil {
		return Embedding{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, embedder.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return Embedding{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := embedder.httpClient.Do(request)
	if err != nil {
		return Embedding{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Embedding{}, fmt.Errorf("embedding service returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Model     string    `json:"model"`
		Version   string    `json:"version"`
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return Embedding{}, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(result.Embedding) == 0 {
		return Embedding{}, fmt.Errorf("embedding service returned an empty vector")
	}
	if strings.TrimSpace(result.Model) == "" {
		result.Model = embedder.model
	}
	return Embedding{Model: result.Model, Version: result.Version, Vector: result.Embedding}, nil
}
