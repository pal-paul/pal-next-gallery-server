package moments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DescriptionJob struct {
	Candidate
	OwnerID          string
	LeaseToken       string
	Attempts         int
	DescriptionReady bool
}

type JobRepository interface {
	ClaimMomentJob(context.Context, int) (DescriptionJob, bool, error)
	CompleteMomentJob(context.Context, DescriptionJob) error
	FailMomentJob(context.Context, DescriptionJob, string) error
	EnqueueMomentCandidates(context.Context, string) (int64, error)
}

type EventPhoto struct {
	Candidate
	Description ImageDescription
	MomentID    string
}

type WorkerRepository interface {
	JobRepository
	ListImageDescriptions(context.Context, []string) ([]ImageDescription, error)
	SaveImageDescriptions(context.Context, []ImageDescription) error
	ListEventPhotos(context.Context, DescriptionJob) ([]EventPhoto, error)
	SaveEventGroup(context.Context, Moment, []Candidate, bool) error
}

type Worker struct {
	embedder    TextEmbedder
	repository  WorkerRepository
	describer   ImageDescriber
	synthesizer MetadataSynthesizer
	enabled     bool
	dailyLimit  int
}

func NewWorker(repository WorkerRepository, describer ImageDescriber, synthesizer MetadataSynthesizer, enabled bool, dailyLimit int) *Worker {
	embedder, _ := describer.(TextEmbedder)
	return &Worker{repository: repository, describer: describer, synthesizer: synthesizer, embedder: embedder, enabled: enabled, dailyLimit: dailyLimit}
}

func WithQueuedGeneration() Option {
	return func(service *Service) { service.queuedGeneration = true }
}

func (worker *Worker) Run(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		processed, err := worker.ProcessOnce(ctx)
		if err != nil && onError != nil {
			onError(err)
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (worker *Worker) ProcessOnce(ctx context.Context) (bool, error) {
	if !worker.enabled || worker.dailyLimit <= 0 || worker.describer == nil || worker.synthesizer == nil {
		return false, nil
	}
	if processed, err := worker.processAlbumMetadataJob(ctx); processed || err != nil {
		return processed, err
	}
	job, found, err := worker.repository.ClaimMomentJob(ctx, worker.dailyLimit)
	if err != nil || !found {
		return found, err
	}
	jobContext, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	err = worker.process(jobContext, job)
	if err == nil {
		err = worker.repository.CompleteMomentJob(jobContext, job)
	}
	if err != nil {
		failureContext, failureCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer failureCancel()
		err = errors.Join(err, worker.repository.FailMomentJob(failureContext, job, err.Error()))
	}
	return true, err
}

func (worker *Worker) process(ctx context.Context, job DescriptionJob) error {
	if err := worker.prepareDescription(ctx, job); err != nil {
		return err
	}
	return worker.evaluateEvents(ctx, job)
}

func (worker *Worker) prepareDescription(ctx context.Context, job DescriptionJob) error {
	stored, err := worker.repository.ListImageDescriptions(ctx, []string{job.ID})
	if err != nil {
		return err
	}
	var description ImageDescription
	if len(stored) > 0 {
		description = stored[0]
	} else {
		if job.DescriptionReady {
			return fmt.Errorf("cached description missing for %s", job.ID)
		}
		description, err = worker.describer.Describe(ctx, job.Candidate)
		if err != nil {
			return err
		}
		if strings.TrimSpace(description.Description) == "" {
			return fmt.Errorf("empty image description for %s", job.ID)
		}
		description.MediaID = job.ID
		if err := worker.repository.SaveImageDescriptions(ctx, []ImageDescription{description}); err != nil {
			return err
		}
	}
	if worker.embedder == nil {
		return nil
	}
	_, valid := TextCosine(description.Embedding, description.Embedding)
	if valid && description.EmbeddingModel == worker.embedder.TextEmbeddingModel() {
		return nil
	}
	embedding, err := worker.embedder.EmbedDescription(ctx, description)
	if err != nil {
		return err
	}
	if _, valid := TextCosine(embedding, embedding); !valid || worker.embedder.TextEmbeddingModel() == "" {
		return fmt.Errorf("invalid descriptor text embedding for %s", job.ID)
	}
	description.Embedding, description.EmbeddingModel = embedding, worker.embedder.TextEmbeddingModel()
	return worker.repository.SaveImageDescriptions(ctx, []ImageDescription{description})
}

func (worker *Worker) evaluateEvents(ctx context.Context, job DescriptionJob) error {
	photos, err := worker.repository.ListEventPhotos(ctx, job)
	if err != nil {
		return err
	}
	for _, group := range clusterEvents(photos) {
		var pending []Candidate
		var candidates []Candidate
		var descriptions []ImageDescription
		for _, photo := range group.photos {
			candidates = append(candidates, photo.Candidate)
			descriptions = append(descriptions, photo.Description)
			if photo.MomentID == "" {
				pending = append(pending, photo.Candidate)
			}
		}
		if len(pending) == 0 || (group.momentID == "" && len(candidates) < 5) {
			continue
		}
		moment := generatedMoment(job.OwnerID, candidates)
		isNew := group.momentID == ""
		if isNew {
			metadata, err := worker.synthesizer.Synthesize(ctx, moment, descriptions)
			if err != nil {
				return err
			}
			if strings.TrimSpace(metadata.Title) == "" || strings.TrimSpace(metadata.Description) == "" {
				return fmt.Errorf("empty event metadata")
			}
			moment.Title, moment.Description = strings.TrimSpace(metadata.Title), strings.TrimSpace(metadata.Description)
		} else {
			moment.ID = group.momentID
		}
		if err := worker.repository.SaveEventGroup(ctx, moment, pending, isNew); err != nil {
			return err
		}
	}
	return nil
}
