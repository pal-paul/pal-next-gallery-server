package moments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultGeminiEndpoint = "https://generativelanguage.googleapis.com/v1beta"

type GeminiEnricher struct {
	endpoint   *url.URL
	apiKey     string
	model      string
	textModel  string
	mediaDir   string
	httpClient *http.Client
}

func NewGeminiPipeline(endpoint, apiKey, visionModel, textModel, mediaDir string) (*GeminiEnricher, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultGeminiEndpoint
	}
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(endpoint), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_GEMINI_URL must be an HTTPS URL")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_GEMINI_API_KEY is required")
	}
	if strings.TrimSpace(visionModel) == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_GEMINI_MODEL is required")
	}
	if strings.TrimSpace(textModel) == "" {
		return nil, fmt.Errorf("ENV_MOMENTS_GEMINI_TEXT_MODEL is required")
	}
	return &GeminiEnricher{
		endpoint: parsed, apiKey: strings.TrimSpace(apiKey), model: strings.TrimSpace(visionModel),
		textModel: strings.TrimSpace(textModel), mediaDir: filepath.Clean(mediaDir), httpClient: http.DefaultClient,
	}, nil
}

func (enricher *GeminiEnricher) Describe(ctx context.Context, candidate Candidate) (ImageDescription, error) {
	imagePath, err := safeMediaPath(enricher.mediaDir, candidate.SourcePath)
	if err != nil {
		return ImageDescription{}, err
	}
	image, err := os.ReadFile(imagePath)
	if err != nil {
		return ImageDescription{}, fmt.Errorf("read representative image: %w", err)
	}
	prompt := `Describe this photo using visible evidence only. Return JSON only with people (array), activities (array), location_type, objects (array), scene, weather, and description. Use empty arrays or empty strings when a detail is not visible. Do not identify people or invent a specific place, date, activity, or weather condition.`
	content, err := enricher.generate(ctx, enricher.model, prompt, &geminiImage{
		MIMEType: imageMIMEType(imagePath, image),
		Data:     base64.StdEncoding.EncodeToString(image),
	}, imageDescriptionSchema())
	if err != nil {
		return ImageDescription{}, err
	}
	var description ImageDescription
	if err := json.Unmarshal([]byte(content), &description); err != nil {
		return ImageDescription{}, fmt.Errorf("decode Gemini image description: %w", err)
	}
	description.Model = enricher.model
	description.Description = strings.TrimSpace(description.Description)
	description.LocationType = strings.TrimSpace(description.LocationType)
	description.Scene = strings.TrimSpace(description.Scene)
	description.Weather = strings.TrimSpace(description.Weather)
	if description.Description == "" {
		return ImageDescription{}, fmt.Errorf("Gemini returned an empty image description")
	}
	return description, nil
}

func (enricher *GeminiEnricher) Synthesize(ctx context.Context, moment Moment, descriptions []ImageDescription) (Metadata, error) {
	structured, err := json.Marshal(descriptions)
	if err != nil {
		return Metadata{}, err
	}
	prompt := fmt.Sprintf(`Create one coherent personal moment from %s to %s. Location metadata: %q. The following JSON descriptions were produced independently from representative photos: %s. Return JSON only with title, description, and confidence. Confidence must be from 0 to 1 and represent the fraction of descriptions supporting the shared title and description. Do not invent details.`,
		moment.StartTime.Format("2006-01-02 15:04"), moment.EndTime.Format("2006-01-02 15:04"), moment.LocationName, structured)
	content, err := enricher.generate(ctx, enricher.textModel, prompt, nil, metadataSchema())
	if err != nil {
		return Metadata{}, err
	}
	return decodeMetadata(content)
}

type geminiImage struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"`
}

func (enricher *GeminiEnricher) generate(ctx context.Context, model, prompt string, image *geminiImage, responseSchema map[string]any) (string, error) {
	type part struct {
		Text       string       `json:"text,omitempty"`
		InlineData *geminiImage `json:"inline_data,omitempty"`
	}
	parts := []part{{Text: prompt}}
	if image != nil {
		parts = append(parts, part{InlineData: image})
	}
	payload := struct {
		Contents []struct {
			Parts []part `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseMIMEType   string         `json:"responseMimeType"`
			ResponseJSONSchema map[string]any `json:"responseJsonSchema"`
		} `json:"generationConfig"`
	}{}
	payload.Contents = append(payload.Contents, struct {
		Parts []part `json:"parts"`
	}{Parts: parts})
	payload.GenerationConfig.ResponseMIMEType = "application/json"
	payload.GenerationConfig.ResponseJSONSchema = responseSchema
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	requestURL := *enricher.endpoint
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/models/" + url.PathEscape(model) + ":generateContent"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-goog-api-key", enricher.apiKey)
	response, err := enricher.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request Gemini: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("Gemini returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("decode Gemini response: %w", err)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 || strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text) == "" {
		return "", fmt.Errorf("Gemini returned an empty response")
	}
	return result.Candidates[0].Content.Parts[0].Text, nil
}

func imageDescriptionSchema() map[string]any {
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"people": stringArray, "activities": stringArray, "location_type": map[string]any{"type": "string"},
			"objects": stringArray, "scene": map[string]any{"type": "string"}, "weather": map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
		},
		"required": []string{"people", "activities", "location_type", "objects", "scene", "weather", "description"},
	}
}

func metadataSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		},
		"required": []string{"title", "description", "confidence"},
	}
}

func imageMIMEType(name string, content []byte) string {
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); detected != "" {
		return strings.SplitN(detected, ";", 2)[0]
	}
	return http.DetectContentType(content)
}

func decodeMetadata(content string) (Metadata, error) {
	var metadata Metadata
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode generated metadata: %w", err)
	}
	metadata.Title = strings.TrimSpace(metadata.Title)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if metadata.Confidence > 1 && metadata.Confidence <= 100 {
		metadata.Confidence /= 100
	}
	if metadata.Title == "" || metadata.Confidence < 0 || metadata.Confidence > 1 {
		return Metadata{}, fmt.Errorf("model returned invalid metadata")
	}
	return metadata, nil
}
