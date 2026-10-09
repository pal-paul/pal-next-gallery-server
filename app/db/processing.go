package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"time"

	"pal-next-gallery-server/app/processing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const nearDuplicateHammingThreshold = 5

func (store *Postgres) ClaimProcessingJob(ctx context.Context) (processing.Job, bool, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return processing.Job{}, false, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var job processing.Job
	err = transaction.QueryRow(ctx, `SELECT job.id, job.upload_id, media.owner_id, media.media_path,
		job.status, job.attempts, COALESCE(job.last_error, ''), job.scheduled_for, job.updated_at
		FROM media_processing_jobs job JOIN media_uploads media ON media.upload_id = job.upload_id
		WHERE ((job.status = 'queued' AND job.scheduled_for <= now()) OR
			(job.status = 'processing' AND job.updated_at <= now() - interval '15 minutes'))
			AND media.deleted_at IS NULL
		ORDER BY job.scheduled_for, job.created_at FOR UPDATE OF job SKIP LOCKED LIMIT 1`).Scan(
		&job.ID, &job.UploadID, &job.OwnerID, &job.MediaPath, &job.Status, &job.Attempts,
		&job.LastError, &job.Scheduled, &job.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return processing.Job{}, false, nil
	}
	if err != nil {
		return processing.Job{}, false, err
	}
	job.Attempts++
	job.Status = "processing"
	if _, err := transaction.Exec(ctx, `UPDATE media_processing_jobs SET status = 'processing', attempts = $2,
		updated_at = now() WHERE id = $1`, job.ID, job.Attempts); err != nil {
		return processing.Job{}, false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return processing.Job{}, false, err
	}
	return job, true, nil
}

func (store *Postgres) CompleteProcessingJob(ctx context.Context, job processing.Job, metadata processing.Metadata) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `UPDATE media_uploads SET thumbnail_path = $2, width = NULLIF($3, 0),
		height = NULLIF($4, 0), video_duration_seconds = NULLIF($5, 0) WHERE upload_id = $1`,
		job.UploadID, metadata.ThumbnailPath, metadata.Width, metadata.Height, metadata.Duration); err != nil {
		return err
	}
	rawEXIF := metadata.RawEXIF
	if len(rawEXIF) == 0 {
		rawEXIF = json.RawMessage(`{}`)
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO media_exif
		(upload_id, captured_at, latitude, longitude, camera_make, camera_model, orientation, perceptual_hash, raw_exif)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (upload_id) DO UPDATE SET
		captured_at = EXCLUDED.captured_at, latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude,
		camera_make = EXCLUDED.camera_make, camera_model = EXCLUDED.camera_model,
		orientation = EXCLUDED.orientation, perceptual_hash = EXCLUDED.perceptual_hash, raw_exif = EXCLUDED.raw_exif`,
		job.UploadID, metadata.CapturedAt, metadata.Latitude, metadata.Longitude, metadata.CameraMake,
		metadata.CameraModel, metadata.Orientation, metadata.PerceptualHash, rawEXIF); err != nil {
		return err
	}
	if err := store.groupNearDuplicate(ctx, transaction, job, metadata.PerceptualHash); err != nil {
		return err
	}
	if len(metadata.Embedding.Vector) > 0 {
		if _, err := transaction.Exec(ctx, `INSERT INTO media_embeddings
			(upload_id, model, version, dimensions, embedding) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (upload_id, model, version) DO UPDATE SET dimensions = EXCLUDED.dimensions,
			embedding = EXCLUDED.embedding, created_at = now()`, job.UploadID, metadata.Embedding.Model,
			metadata.Embedding.Version, len(metadata.Embedding.Vector), metadata.Embedding.Vector); err != nil {
			return err
		}
	}
	if _, err := transaction.Exec(ctx, `UPDATE media_processing_jobs SET status = 'completed', last_error = NULL,
		updated_at = now() WHERE id = $1`, job.ID); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) groupNearDuplicate(ctx context.Context, transaction pgx.Tx, job processing.Job, hash string) error {
	if hash == "" {
		return nil
	}
	rows, err := transaction.Query(ctx, `SELECT media.upload_id, exif.perceptual_hash
		FROM media_uploads media JOIN media_exif exif ON exif.upload_id = media.upload_id
		WHERE media.owner_id = $1 AND media.upload_id <> $2 AND media.deleted_at IS NULL
		AND exif.perceptual_hash <> ''`, job.OwnerID, job.UploadID)
	if err != nil {
		return err
	}
	defer rows.Close()
	nearestID := ""
	nearestDistance := 65
	for rows.Next() {
		var uploadID, candidateHash string
		if err := rows.Scan(&uploadID, &candidateHash); err != nil {
			return err
		}
		distance, err := differenceHashDistance(hash, candidateHash)
		if err == nil && distance < nearestDistance {
			nearestID, nearestDistance = uploadID, distance
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if nearestDistance > nearDuplicateHammingThreshold {
		return nil
	}
	var groupID string
	err = transaction.QueryRow(ctx, `SELECT duplicate.group_id
		FROM media_duplicate_group_members duplicate
		JOIN media_duplicate_groups group_record ON group_record.id = duplicate.group_id
		WHERE group_record.owner_id = $1 AND group_record.kind = 'near' AND duplicate.upload_id = $2
		LIMIT 1`, job.OwnerID, nearestID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		groupID = uuid.NewString()
		if _, err := transaction.Exec(ctx, `INSERT INTO media_duplicate_groups (id, owner_id, kind, primary_upload_id)
			VALUES ($1, $2, 'near', $3)`, groupID, job.OwnerID, nearestID); err != nil {
			return err
		}
		if _, err := transaction.Exec(ctx, `INSERT INTO media_duplicate_group_members
			(group_id, upload_id, hamming_distance, is_primary) VALUES ($1, $2, 0, TRUE)`, groupID, nearestID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = transaction.Exec(ctx, `INSERT INTO media_duplicate_group_members
		(group_id, upload_id, hamming_distance, is_primary) VALUES ($1, $2, $3, FALSE)
		ON CONFLICT (group_id, upload_id) DO UPDATE SET hamming_distance = EXCLUDED.hamming_distance`,
		groupID, job.UploadID, nearestDistance)
	return err
}

func differenceHashDistance(left, right string) (int, error) {
	leftHash, err := strconv.ParseUint(left, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("decode left perceptual hash: %w", err)
	}
	rightHash, err := strconv.ParseUint(right, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("decode right perceptual hash: %w", err)
	}
	return bits.OnesCount64(leftHash ^ rightHash), nil
}

func (store *Postgres) FailProcessingJob(ctx context.Context, job processing.Job, message string) error {
	status := "queued"
	scheduled := time.Now().Add(time.Duration(1<<min(job.Attempts, 6)) * 30 * time.Second)
	if job.Attempts >= 3 {
		status = "failed"
	}
	_, err := store.pool.Exec(ctx, `UPDATE media_processing_jobs SET status = $2, last_error = $3,
		scheduled_for = $4, updated_at = now() WHERE id = $1`, job.ID, status, message, scheduled)
	return err
}

func (store *Postgres) GetProcessingStatus(ctx context.Context, uploadID, userID string) (processing.Job, error) {
	var job processing.Job
	err := store.pool.QueryRow(ctx, `SELECT job.id, job.upload_id, job.status, job.attempts,
		COALESCE(job.last_error, ''), job.scheduled_for, job.updated_at
		FROM media_processing_jobs job JOIN media_uploads media ON media.upload_id = job.upload_id
		WHERE job.upload_id = $1 AND (media.owner_id = $2 OR EXISTS (SELECT 1 FROM user_media_shares share
		WHERE share.owner_id = media.owner_id AND share.user_id = $2))`, uploadID, userID).Scan(
		&job.ID, &job.UploadID, &job.Status, &job.Attempts, &job.LastError, &job.Scheduled, &job.UpdatedAt)
	return job, err
}

func (store *Postgres) ListProcessingJobs(ctx context.Context) ([]processing.Job, error) {
	rows, err := store.pool.Query(ctx, `SELECT id, upload_id, status, attempts, COALESCE(last_error, ''),
		scheduled_for, updated_at FROM media_processing_jobs ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []processing.Job
	for rows.Next() {
		var job processing.Job
		if err := rows.Scan(&job.ID, &job.UploadID, &job.Status, &job.Attempts, &job.LastError, &job.Scheduled, &job.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (store *Postgres) RetryProcessingJob(ctx context.Context, jobID string) error {
	result, err := store.pool.Exec(ctx, `UPDATE media_processing_jobs SET status = 'queued', attempts = 0,
		last_error = NULL, scheduled_for = now(), updated_at = now() WHERE id = $1 AND status = 'failed'`, jobID)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("failed processing job not found")
	}
	return err
}
