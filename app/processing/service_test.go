package processing

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
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
