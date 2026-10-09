package processing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPImageEmbedderSendsImageAndParsesVector(t *testing.T) {
	imageBytes := []byte("image bytes")
	imagePath := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Model string `json:"model"`
			Image string `json:"image"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "siglip-so400m" || payload.Image != base64.StdEncoding.EncodeToString(imageBytes) {
			t.Errorf("unexpected embedding payload: %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"siglip-so400m","version":"v1","embedding":[0.1,-0.2,0.3]}`))
	}))
	defer server.Close()

	embedder, err := NewHTTPImageEmbedder(server.URL, "siglip-so400m")
	if err != nil {
		t.Fatal(err)
	}
	embedding, err := embedder.Embed(context.Background(), imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if embedding.Model != "siglip-so400m" || embedding.Version != "v1" || len(embedding.Vector) != 3 || embedding.Vector[1] != -0.2 {
		t.Fatalf("unexpected embedding: %#v", embedding)
	}
}
