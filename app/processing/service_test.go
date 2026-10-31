package processing

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type jobRepository struct {
	job                 Job
	available           bool
	completed           bool
	failed              bool
	completeErr         error
	failureContextError error
}

func (repository *jobRepository) ClaimProcessingJob(context.Context) (Job, bool, error) {
	if !repository.available {
		return Job{}, false, nil
	}
	repository.available = false
	return repository.job, true, nil
}
func (repository *jobRepository) CompleteProcessingJob(context.Context, Job, Metadata) error {
	repository.completed = true
	return repository.completeErr
}
func (repository *jobRepository) FailProcessingJob(ctx context.Context, _ Job, _ string) error {
	repository.failed = true
	repository.failureContextError = ctx.Err()
	return nil
}
func (*jobRepository) GetProcessingStatus(context.Context, string, string) (Job, error) {
	return Job{}, nil
}
func (*jobRepository) ListProcessingJobs(context.Context) ([]Job, error) { return nil, nil }
func (*jobRepository) RetryProcessingJob(context.Context, string) error  { return nil }

func TestProcessOnceCompletesOrRetriesJob(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "complete"},
		{name: "retry", err: errors.New("ffmpeg failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &jobRepository{job: Job{ID: "job"}, available: true}
			service := New(repository, t.TempDir())
			service.process = func(context.Context, Job) (Metadata, error) { return Metadata{}, test.err }
			processed, err := service.ProcessOnce(context.Background())
			if !processed {
				t.Fatal("expected a claimed job")
			}
			if test.err == nil && (err != nil || !repository.completed || repository.failed) {
				t.Fatalf("completion state: err=%v complete=%t failed=%t", err, repository.completed, repository.failed)
			}
			if test.err != nil && (err == nil || repository.completed || !repository.failed) {
				t.Fatalf("retry state: err=%v complete=%t failed=%t", err, repository.completed, repository.failed)
			}
		})
	}
}

func TestProcessHEIFSample(t *testing.T) {
	samplePath := os.Getenv("GALLERY_HEIF_TEST_FILE")
	if samplePath == "" {
		t.Skip("set GALLERY_HEIF_TEST_FILE to validate a representative HEIF photo")
	}
	sample, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.heic"), sample, 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(nil, root)
	metadata, err := service.processMedia(context.Background(), Job{OwnerID: "owner", UploadID: "sample", MediaPath: "sample.heic"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(root, metadata.ThumbnailPath))
	if err != nil {
		t.Fatal(err)
	}
	thumbnail, decodeErr := jpeg.Decode(file)
	if err := errors.Join(decodeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	bounds := thumbnail.Bounds()
	if bounds.Dx() > 640 || bounds.Dy() > 640 || metadata.Width < bounds.Dx() || metadata.Height < bounds.Dy() {
		t.Fatalf("invalid thumbnail dimensions: %v, primary: %dx%d", bounds, metadata.Width, metadata.Height)
	}
	colorPixels := 0
	for row := bounds.Min.Y; row < bounds.Max.Y; row++ {
		for column := bounds.Min.X; column < bounds.Max.X; column++ {
			red, green, blue, _ := thumbnail.At(column, row).RGBA()
			if max(red, green, blue)-min(red, green, blue) > 4096 {
				colorPixels++
			}
		}
	}
	if colorPixels < bounds.Dx()*bounds.Dy()/10 {
		t.Fatalf("representative color photo became monochrome: %d color pixels", colorPixels)
	}
	t.Logf("primary: %dx%d, thumbnail: %dx%d, color pixels: %d", metadata.Width, metadata.Height, bounds.Dx(), bounds.Dy(), colorPixels)
}

func TestProcessOnceRecordsFailureWithIndependentContext(t *testing.T) {
	repository := &jobRepository{job: Job{ID: "job"}, available: true}
	service := New(repository, t.TempDir())
	service.process = func(context.Context, Job) (Metadata, error) {
		return Metadata{}, context.Canceled
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed, err := service.ProcessOnce(ctx)

	if !processed || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled claimed job, processed=%t err=%v", processed, err)
	}
	if !repository.failed || repository.failureContextError != nil {
		t.Fatalf("failure state: failed=%t contextErr=%v", repository.failed, repository.failureContextError)
	}
}

func TestProcessOnceRequeuesCompletionFailure(t *testing.T) {
	completionErr := errors.New("database unavailable")
	repository := &jobRepository{job: Job{ID: "job"}, available: true, completeErr: completionErr}
	service := New(repository, t.TempDir())
	service.process = func(context.Context, Job) (Metadata, error) { return Metadata{}, nil }

	processed, err := service.ProcessOnce(context.Background())

	if !processed || !errors.Is(err, completionErr) {
		t.Fatalf("expected completion failure, processed=%t err=%v", processed, err)
	}
	if !repository.completed || !repository.failed {
		t.Fatalf("completion state: completed=%t failed=%t", repository.completed, repository.failed)
	}
}

func TestFirstTagReturnsFirstNonEmptyValue(t *testing.T) {
	tags := map[string]string{"make": " ", "camera_make": "Fujifilm", "model": "X-T5"}
	if got := firstTag(tags, "make", "camera_make"); got != "Fujifilm" {
		t.Fatalf("unexpected camera make: %q", got)
	}
}

func TestProcessHEIFPreservesColor(t *testing.T) {
	for _, executable := range []string{"magick", "ffprobe", "ffmpeg"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("%s is required for HEIF integration test", executable)
		}
	}
	root := t.TempDir()
	source := image.NewRGBA(image.Rect(0, 0, 900, 300))
	colors := []color.RGBA{{R: 240, G: 20, B: 20, A: 255}, {R: 20, G: 240, B: 20, A: 255}, {R: 20, G: 20, B: 240, A: 255}}
	for row := 0; row < source.Bounds().Dy(); row++ {
		for column := 0; column < source.Bounds().Dx(); column++ {
			source.SetRGBA(column, row, colors[column/300])
		}
	}
	file, err := os.Create(filepath.Join(root, "source.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := jpeg.Encode(file, source, &jpeg.Options{Quality: 100})
	if err := errors.Join(encodeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	for _, extension := range []string{".HEIC", ".heif"} {
		t.Run(extension, func(t *testing.T) {
			input := filepath.Join(root, "photo"+extension)
			command := exec.Command("magick", "-limit", "thread", "2", filepath.Join(root, "source.jpg"), "HEIC:"+input)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("encode HEIF: %v: %s", err, output)
			}
			service := New(nil, root)
			metadata, err := service.processMedia(context.Background(), Job{OwnerID: "owner", UploadID: "photo", MediaPath: "photo" + extension})
			if err != nil {
				t.Fatal(err)
			}
			if metadata.Width != 900 || metadata.Height != 300 || metadata.Duration != 0 {
				t.Fatalf("unexpected primary image metadata: %+v", metadata)
			}
			thumbnailFile, err := os.Open(filepath.Join(root, metadata.ThumbnailPath))
			if err != nil {
				t.Fatal(err)
			}
			thumbnail, decodeErr := jpeg.Decode(thumbnailFile)
			if err := errors.Join(decodeErr, thumbnailFile.Close()); err != nil {
				t.Fatal(err)
			}
			bounds := thumbnail.Bounds()
			if bounds.Dx() > 640 || bounds.Dy() > 640 {
				t.Fatalf("thumbnail too large: %v", bounds)
			}
			for index := range colors {
				red, green, blue, _ := thumbnail.At((2*index+1)*bounds.Dx()/6, bounds.Dy()/2).RGBA()
				channels := []uint32{red, green, blue}
				for other := range channels {
					if other != index && channels[index] <= channels[other]*3 {
						t.Fatalf("color patch %d lost its color: %v", index, channels)
					}
				}
			}
		})
	}
}

func TestDifferenceHashFileDetectsHorizontalGradient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gradient.jpg")
	gradient := image.NewGray(image.Rect(0, 0, 90, 80))
	for y := 0; y < gradient.Bounds().Dy(); y++ {
		for x := 0; x < gradient.Bounds().Dx(); x++ {
			gradient.SetGray(x, y, color.Gray{Y: uint8(255 - x*255/gradient.Bounds().Dx())})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(file, gradient, &jpeg.Options{Quality: 100}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	hash, err := differenceHashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "ffffffffffffffff" {
		t.Fatalf("unexpected difference hash: %s", hash)
	}
}
