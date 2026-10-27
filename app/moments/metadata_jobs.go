package moments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AlbumMetadataJob struct {
	ID         string    `json:"id"`
	AlbumID    string    `json:"albumId"`
	OwnerID    string    `json:"-"`
	Status     string    `json:"status"`
	Result     *Metadata `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
	LeaseToken string    `json:"-"`
}

type AlbumMetadataJobRepository interface {
	EnqueueAlbumMetadataJob(context.Context, string, string) (AlbumMetadataJob, error)
	GetAlbumMetadataJob(context.Context, string, string, string) (AlbumMetadataJob, error)
	ClaimAlbumMetadataJob(context.Context) (AlbumMetadataJob, bool, error)
	FinishAlbumMetadataJob(context.Context, AlbumMetadataJob, *Metadata, string, bool) error
	ListAlbumMetadataCandidates(context.Context, string, string, int) ([]Candidate, error)
}

func (service *Service) GetAlbumMetadataSuggestion(ctx *gin.Context) {
	if !validAlbumJobIDs(ctx) {
		return
	}
	repository, ok := service.repository.(AlbumMetadataJobRepository)
	if !ok {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "metadata queue is unavailable"})
		return
	}
	job, err := repository.GetAlbumMetadataJob(ctx.Request.Context(), ctx.Param("jobID"), ctx.Param("id"), currentUser(ctx).ID)
	respond(ctx, job, err)
}

func (worker *Worker) processAlbumMetadataJob(ctx context.Context) (bool, error) {
	repository, ok := worker.repository.(interface {
		AlbumMetadataJobRepository
		Repository
	})
	if !ok {
		return false, nil
	}
	job, found, err := repository.ClaimAlbumMetadataJob(ctx)
	if err != nil || !found {
		return found, err
	}
	workContext, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	candidates, err := repository.ListAlbumMetadataCandidates(workContext, job.AlbumID, job.OwnerID, MetadataSampleSize)
	var metadata *Metadata
	waiting := false
	if err == nil && len(candidates) == 0 {
		err = fmt.Errorf("at least one accessible image is required")
	}
	if err == nil {
		service := New(repository, WithQueuedGeneration(), WithImageDescriber(worker.describer), WithMetadataSynthesizer(worker.synthesizer))
		var descriptions []ImageDescription
		descriptions, err = service.describeCandidates(workContext, candidates)
		if err != nil {
			waiting = errors.Is(err, errDescriptionPending)
		} else {
			var result Metadata
			result, err = worker.synthesizer.Synthesize(workContext, generatedMoment(job.OwnerID, candidates), descriptions)
			if err == nil && (strings.TrimSpace(result.Title) == "" || strings.TrimSpace(result.Description) == "") {
				err = fmt.Errorf("empty album metadata")
			}
			if err == nil {
				metadata = &result
			}
		}
	}
	errorMessage := ""
	if err != nil {
		errorMessage = "Unable to generate album metadata"
		if waiting {
			errorMessage = "Waiting for cached image descriptions from the moment worker"
		}
	}
	finishContext, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	finishErr := repository.FinishAlbumMetadataJob(finishContext, job, metadata, errorMessage, waiting)
	if waiting {
		return false, finishErr
	}
	return true, errors.Join(err, finishErr)
}

func validAlbumJobIDs(ctx *gin.Context) bool {
	if _, err := uuid.Parse(ctx.Param("id")); err != nil {
		respond(ctx, nil, ErrNotFound)
		return false
	}
	if jobID := ctx.Param("jobID"); jobID != "" {
		if _, err := uuid.Parse(jobID); err != nil {
			respond(ctx, nil, ErrNotFound)
			return false
		}
	}
	return true
}
