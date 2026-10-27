package store

import (
	"context"
	"errors"
	"fmt"

	"pal-next-gallery-server/app/moments"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const enqueueMomentCandidatesQuery = `INSERT INTO moment_description_jobs (upload_id)
	SELECT media.upload_id FROM media_uploads media
	WHERE ($1 = '' OR media.owner_id::text = $1) AND media.deleted_at IS NULL
	AND media.moments_enabled AND media.mime_type LIKE 'image/%'
	AND EXISTS (SELECT 1 FROM media_processing_jobs job WHERE job.upload_id = media.upload_id AND job.status = 'completed')
	AND NOT EXISTS (SELECT 1 FROM moment_media member JOIN moments moment ON moment.id = member.moment_id
		WHERE member.upload_id = media.upload_id AND moment.owner_id = media.owner_id)
	AND NOT EXISTS (SELECT 1 FROM media_duplicate_group_members duplicate
		WHERE duplicate.upload_id = media.upload_id AND NOT duplicate.is_primary)
	ON CONFLICT DO NOTHING`

func (store *Postgres) EnqueueMomentCandidates(ctx context.Context, ownerID string) (int64, error) {
	result, err := store.pool.Exec(ctx, enqueueMomentCandidatesQuery, ownerID)
	return result.RowsAffected(), err
}

func (store *Postgres) EnqueueMomentBacklog(ctx context.Context, embeddingModel string) (int64, error) {
	result, err := store.pool.Exec(ctx, `INSERT INTO moment_description_jobs (upload_id)
		SELECT media.upload_id FROM media_uploads media
		LEFT JOIN LATERAL (SELECT stored.* FROM media_image_descriptions stored
			WHERE stored.upload_id = media.upload_id ORDER BY stored.created_at DESC LIMIT 1) description ON TRUE
		WHERE media.deleted_at IS NULL AND media.moments_enabled AND media.mime_type LIKE 'image/%'
		AND EXISTS (SELECT 1 FROM media_processing_jobs job WHERE job.upload_id = media.upload_id AND job.status = 'completed')
		AND NOT EXISTS (SELECT 1 FROM media_duplicate_group_members duplicate
			WHERE duplicate.upload_id = media.upload_id AND NOT duplicate.is_primary)
		AND (description.upload_id IS NULL OR description.text_embedding_model <> $1
			OR gallery_text_cosine(description.text_embedding, description.text_embedding) IS NULL)
		ON CONFLICT DO NOTHING`, embeddingModel)
	return result.RowsAffected(), err
}

func (store *Postgres) ClaimMomentJob(ctx context.Context, dailyLimit int) (moments.DescriptionJob, bool, error) {
	if dailyLimit <= 0 {
		return moments.DescriptionJob{}, false, nil
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return moments.DescriptionJob{}, false, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(87143902)`); err != nil {
		return moments.DescriptionJob{}, false, err
	}
	var job moments.DescriptionJob
	err = transaction.QueryRow(ctx, `SELECT media.upload_id, media.owner_id, COALESCE(media.thumbnail_path, media.media_path),
		COALESCE(exif.captured_at, media.created_at), exif.latitude, exif.longitude, job.attempts,
		EXISTS (SELECT 1 FROM media_image_descriptions description WHERE description.upload_id = media.upload_id)
		FROM moment_description_jobs job JOIN media_uploads media ON media.upload_id = job.upload_id
		LEFT JOIN media_exif exif ON exif.upload_id = media.upload_id
		WHERE job.scheduled_for <= now() AND (job.lease_until IS NULL OR job.lease_until <= now())
		AND media.deleted_at IS NULL AND media.moments_enabled AND media.mime_type LIKE 'image/%'
		AND NOT EXISTS (SELECT 1 FROM moment_description_jobs active_job
			JOIN media_uploads active_media ON active_media.upload_id = active_job.upload_id
			WHERE active_media.owner_id = media.owner_id AND active_job.upload_id <> job.upload_id
			AND active_job.lease_until > now())
		AND NOT EXISTS (SELECT 1 FROM media_duplicate_group_members duplicate
			WHERE duplicate.upload_id = media.upload_id AND NOT duplicate.is_primary)
		AND (EXISTS (SELECT 1 FROM media_image_descriptions description WHERE description.upload_id = media.upload_id)
			OR COALESCE((SELECT image_attempts FROM moment_daily_usage WHERE usage_day = (now() AT TIME ZONE 'UTC')::date), 0) < $1)
		ORDER BY job.scheduled_for, job.created_at, job.upload_id FOR UPDATE OF job SKIP LOCKED LIMIT 1`, dailyLimit).Scan(
		&job.ID, &job.OwnerID, &job.SourcePath, &job.CapturedAt, &job.Latitude, &job.Longitude, &job.Attempts, &job.DescriptionReady)
	if errors.Is(err, pgx.ErrNoRows) {
		return moments.DescriptionJob{}, false, nil
	}
	if err != nil {
		return moments.DescriptionJob{}, false, err
	}
	if !job.DescriptionReady {
		if _, err := transaction.Exec(ctx, `INSERT INTO moment_daily_usage (usage_day, image_attempts)
			VALUES ((now() AT TIME ZONE 'UTC')::date, 1) ON CONFLICT (usage_day)
			DO UPDATE SET image_attempts = moment_daily_usage.image_attempts + 1`); err != nil {
			return moments.DescriptionJob{}, false, err
		}
	}
	job.LeaseToken = uuid.NewString()
	job.Attempts++
	if _, err := transaction.Exec(ctx, `UPDATE moment_description_jobs SET lease_token = $2,
		lease_until = now() + interval '15 minutes', attempts = attempts + 1 WHERE upload_id = $1`, job.ID, job.LeaseToken); err != nil {
		return moments.DescriptionJob{}, false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return moments.DescriptionJob{}, false, err
	}
	return job, true, nil
}

func (store *Postgres) CompleteMomentJob(ctx context.Context, job moments.DescriptionJob) error {
	result, err := store.pool.Exec(ctx, `DELETE FROM moment_description_jobs WHERE upload_id = $1 AND lease_token = $2`, job.ID, job.LeaseToken)
	if err == nil && result.RowsAffected() != 1 {
		return fmt.Errorf("moment job lease lost for %s", job.ID)
	}
	return err
}

func (store *Postgres) FailMomentJob(ctx context.Context, job moments.DescriptionJob, message string) error {
	_, err := store.pool.Exec(ctx, `UPDATE moment_description_jobs SET lease_token = NULL, lease_until = NULL,
		last_error = $3, scheduled_for = now() + make_interval(secs => LEAST(attempts, 60) * 60)
		WHERE upload_id = $1 AND lease_token = $2`, job.ID, job.LeaseToken, message)
	return err
}
