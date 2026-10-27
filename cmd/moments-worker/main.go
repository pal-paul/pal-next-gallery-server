package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	store "pal-next-gallery-server/app/db"
	"pal-next-gallery-server/app/moments"
)

func main() {
	if err := run(); err != nil {
		slog.Error("moment worker stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	aiEnabled, err := enabled("ENV_AI_FEATURE", "NO")
	if err != nil {
		return err
	}
	limit, err := strconv.Atoi(setting("ENV_MOMENTS_DAILY_IMAGE_LIMIT", "100"))
	if err != nil || limit < 0 {
		return fmt.Errorf("ENV_MOMENTS_DAILY_IMAGE_LIMIT must be a nonnegative integer")
	}
	if !aiEnabled || limit == 0 {
		slog.Info("moment inference disabled; queue retained")
		<-ctx.Done()
		return nil
	}
	if strings.TrimSpace(os.Getenv("ENV_DATABASE_URL")) == "" {
		return fmt.Errorf("ENV_DATABASE_URL is required")
	}
	pipeline, err := moments.NewGeminiPipeline(setting("ENV_MOMENTS_GEMINI_URL", "https://generativelanguage.googleapis.com/v1beta"),
		os.Getenv("ENV_MOMENTS_GEMINI_API_KEY"), setting("ENV_MOMENTS_GEMINI_MODEL", "gemini-3.5-flash-lite"),
		setting("ENV_MOMENTS_GEMINI_TEXT_MODEL", "gemini-3.5-flash-lite"), setting("ENV_MEDIA_DIR", "./vol/medias"))
	if err != nil {
		return err
	}
	if err := pipeline.SetTextEmbeddingModel(setting("ENV_MOMENTS_GEMINI_EMBEDDING_MODEL", "gemini-embedding-001")); err != nil {
		return err
	}
	database, err := store.NewPostgres(ctx, os.Getenv("ENV_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return err
	}
	queued, err := database.EnqueueMomentBacklog(ctx, pipeline.TextEmbeddingModel())
	if err != nil {
		return fmt.Errorf("queue existing moment photos: %w", err)
	}
	slog.Info("moment backlog discovered", "queued", queued)
	slog.Info("moment worker started", "daily_image_limit", limit, "quota_timezone", "UTC", "text_embedding_model", pipeline.TextEmbeddingModel())
	moments.NewWorker(database, pipeline, pipeline, true, limit).Run(ctx, func(err error) {
		slog.Error("moment job failed", "error", err)
	})
	return nil
}

func setting(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func enabled(name, fallback string) (bool, error) {
	switch strings.ToUpper(setting(name, fallback)) {
	case "YES":
		return true, nil
	case "NO":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be YES or NO", name)
	}
}
