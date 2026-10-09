package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Job struct {
	ID        string    `json:"id"`
	UploadID  string    `json:"uploadId"`
	OwnerID   string    `json:"ownerId,omitempty"`
	MediaPath string    `json:"-"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"lastError,omitempty"`
	Scheduled time.Time `json:"scheduledFor"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Metadata struct {
	ThumbnailPath  string
	Width          int
	Height         int
	Duration       float64
	RawEXIF        json.RawMessage
	CapturedAt     *time.Time
	Latitude       *float64
	Longitude      *float64
	CameraMake     string
	CameraModel    string
	Orientation    string
	PerceptualHash string
	Embedding      Embedding
}

type Repository interface {
	ClaimProcessingJob(context.Context) (Job, bool, error)
	CompleteProcessingJob(context.Context, Job, Metadata) error
	FailProcessingJob(context.Context, Job, string) error
	GetProcessingStatus(context.Context, string, string) (Job, error)
	ListProcessingJobs(context.Context) ([]Job, error)
	RetryProcessingJob(context.Context, string) error
}

type Service struct {
	repository Repository
	mediaDir   string
	process    func(context.Context, Job) (Metadata, error)
	embedder   ImageEmbedder
}

type Option func(*Service)

func WithImageEmbedder(embedder ImageEmbedder) Option {
	return func(service *Service) { service.embedder = embedder }
}

func New(repository Repository, mediaDir string, options ...Option) *Service {
	service := &Service{repository: repository, mediaDir: filepath.Clean(mediaDir)}
	service.process = service.processMedia
	for _, option := range options {
		option(service)
	}
	return service
}

func (service *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		processed, _ := service.ProcessOnce(ctx)
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (service *Service) ProcessOnce(ctx context.Context) (bool, error) {
	job, found, err := service.repository.ClaimProcessingJob(ctx)
	if err != nil || !found {
		return found, err
	}
	metadata, err := service.process(ctx, job)
	if err != nil {
		if failErr := service.recordFailure(job, err); failErr != nil {
			return true, fmt.Errorf("process media: %v; record failure: %w", err, failErr)
		}
		return true, err
	}
	if service.embedder != nil {
		thumbnail, pathErr := service.safePath(metadata.ThumbnailPath)
		if pathErr != nil {
			return true, service.failJob(job, pathErr)
		}
		metadata.Embedding, err = service.embedder.Embed(ctx, thumbnail)
		if err != nil {
			return true, service.failJob(job, fmt.Errorf("embed image: %w", err))
		}
	}
	if err := service.repository.CompleteProcessingJob(ctx, job, metadata); err != nil {
		if failErr := service.recordFailure(job, fmt.Errorf("complete processing job: %w", err)); failErr != nil {
			return true, fmt.Errorf("complete processing job: %v; record failure: %w", err, failErr)
		}
		return true, err
	}
	return true, nil
}

func (service *Service) failJob(job Job, err error) error {
	if failErr := service.recordFailure(job, err); failErr != nil {
		return fmt.Errorf("%v; record failure: %w", err, failErr)
	}
	return err
}

func (service *Service) recordFailure(job Job, processingErr error) error {
	failureContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.repository.FailProcessingJob(failureContext, job, processingErr.Error())
}

func (service *Service) processMedia(ctx context.Context, job Job) (Metadata, error) {
	input, err := service.safePath(job.MediaPath)
	if err != nil {
		return Metadata{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", input).Output()
	if err != nil {
		return Metadata{}, fmt.Errorf("ffprobe: %w", err)
	}
	var probe struct {
		Streams []struct {
			Width  int               `json:"width"`
			Height int               `json:"height"`
			Tags   map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &probe); err != nil {
		return Metadata{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	metadata := Metadata{RawEXIF: output}
	for _, stream := range probe.Streams {
		if stream.Width > 0 && stream.Height > 0 {
			metadata.Width, metadata.Height = stream.Width, stream.Height
			break
		}
	}
	metadata.Duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	tags := make(map[string]string)
	for key, value := range probe.Format.Tags {
		tags[strings.ToLower(key)] = value
	}
	for _, stream := range probe.Streams {
		for key, value := range stream.Tags {
			tags[strings.ToLower(key)] = value
		}
	}
	metadata.CameraMake = firstTag(tags, "make", "camera_make", "com.apple.quicktime.make")
	metadata.CameraModel = firstTag(tags, "model", "camera_model", "com.apple.quicktime.model")
	metadata.Orientation = firstTag(tags, "orientation", "rotate")
	for _, key := range []string{"creation_time", "date", "datetimeoriginal"} {
		if value := tags[key]; value != "" {
			for _, layout := range []string{time.RFC3339, "2006:01:02 15:04:05"} {
				parsed, parseErr := time.Parse(layout, value)
				if parseErr != nil {
					continue
				}
				metadata.CapturedAt = &parsed
				break
			}
		}
		if metadata.CapturedAt != nil {
			break
		}
	}
	if latitude, err := strconv.ParseFloat(tags["gpslatitude"], 64); err == nil && latitude >= -90 && latitude <= 90 {
		metadata.Latitude = &latitude
	}
	if longitude, err := strconv.ParseFloat(tags["gpslongitude"], 64); err == nil && longitude >= -180 && longitude <= 180 {
		metadata.Longitude = &longitude
	}
	if metadata.Latitude == nil || metadata.Longitude == nil {
		locationPattern := regexp.MustCompile(`^([+-][0-9]+(?:\.[0-9]+)?)([+-][0-9]+(?:\.[0-9]+)?)`)
		if match := locationPattern.FindStringSubmatch(tags["location"]); len(match) == 3 {
			latitude, latitudeErr := strconv.ParseFloat(match[1], 64)
			longitude, longitudeErr := strconv.ParseFloat(match[2], 64)
			if latitudeErr == nil && longitudeErr == nil && latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180 {
				metadata.Latitude, metadata.Longitude = &latitude, &longitude
			}
		}
	}
	thumbnailRelative := filepath.ToSlash(filepath.Join(".thumbnails", job.OwnerID, job.UploadID+".jpg"))
	thumbnail, err := service.safePath(thumbnailRelative)
	if err != nil {
		return Metadata{}, err
	}
	if err := os.MkdirAll(filepath.Dir(thumbnail), 0o750); err != nil {
		return Metadata{}, err
	}
	thumbnailCtx, thumbnailCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer thumbnailCancel()
	command := exec.CommandContext(thumbnailCtx, "ffmpeg", "-nostdin", "-y", "-i", input, "-vf", "thumbnail,scale=640:640:force_original_aspect_ratio=decrease", "-frames:v", "1", thumbnail)
	if combined, err := command.CombinedOutput(); err != nil {
		return Metadata{}, fmt.Errorf("ffmpeg thumbnail: %w: %s", err, strings.TrimSpace(string(combined)))
	}
	metadata.ThumbnailPath = thumbnailRelative
	metadata.PerceptualHash, err = differenceHashFile(thumbnail)
	if err != nil {
		return Metadata{}, fmt.Errorf("calculate perceptual hash: %w", err)
	}
	return metadata, nil
}

func firstTag(tags map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(tags[key]); value != "" {
			return value
		}
	}
	return ""
}

func differenceHashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	image, err := jpeg.Decode(file)
	if err != nil {
		return "", err
	}
	bounds := image.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return "", errors.New("empty thumbnail")
	}
	var hash uint64
	for row := 0; row < 8; row++ {
		y := bounds.Min.Y + row*bounds.Dy()/8
		for column := 0; column < 8; column++ {
			leftX := bounds.Min.X + column*bounds.Dx()/9
			rightX := bounds.Min.X + (column+1)*bounds.Dx()/9
			leftR, leftG, leftB, _ := image.At(leftX, y).RGBA()
			rightR, rightG, rightB, _ := image.At(rightX, y).RGBA()
			leftLuma := 299*uint64(leftR) + 587*uint64(leftG) + 114*uint64(leftB)
			rightLuma := 299*uint64(rightR) + 587*uint64(rightG) + 114*uint64(rightB)
			if leftLuma > rightLuma {
				hash |= 1 << uint(row*8+column)
			}
		}
	}
	return fmt.Sprintf("%016x", hash), nil
}

func (service *Service) safePath(relative string) (string, error) {
	path := filepath.Join(service.mediaDir, filepath.FromSlash(relative))
	resolved, err := filepath.Rel(service.mediaDir, path)
	if err != nil || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) || filepath.IsAbs(resolved) {
		return "", errors.New("invalid media path")
	}
	return path, nil
}
