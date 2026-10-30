package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"pal-next-gallery-server/app/moments"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func scanAlbumMetadataJob(row pgx.Row) (moments.AlbumMetadataJob, error) {
	var job moments.AlbumMetadataJob
	var result []byte
	err := row.Scan(&job.ID, &job.AlbumID, &job.OwnerID, &job.Status, &result, &job.Error, &job.LeaseToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, moments.ErrNotFound
	}
	if err == nil && len(result) > 0 {
		err = json.Unmarshal(result, &job.Result)
	}
	return job, err
}

func (store *Postgres) EnqueueAlbumMetadataJob(ctx context.Context, albumID, ownerID string) (moments.AlbumMetadataJob, error) {
	return scanAlbumMetadataJob(store.pool.QueryRow(ctx, `INSERT INTO album_metadata_jobs (id, album_id, owner_id)
		SELECT $1, id, owner_id FROM albums WHERE id = $2 AND owner_id = $3
		ON CONFLICT (album_id, owner_id) WHERE status IN ('pending', 'running')
		DO UPDATE SET album_id = EXCLUDED.album_id
		RETURNING id, album_id, owner_id, status, result, error, COALESCE(lease_token, '')`, uuid.NewString(), albumID, ownerID))
}

func (store *Postgres) GetAlbumMetadataJob(ctx context.Context, jobID, albumID, ownerID string) (moments.AlbumMetadataJob, error) {
	return scanAlbumMetadataJob(store.pool.QueryRow(ctx, `SELECT job.id, job.album_id, job.owner_id, job.status, job.result, job.error,
		COALESCE(job.lease_token, '') FROM album_metadata_jobs job JOIN albums album ON album.id = job.album_id
		WHERE job.id = $1 AND job.album_id = $2 AND job.owner_id = $3 AND album.owner_id = $3`, jobID, albumID, ownerID))
}

func (store *Postgres) ClaimAlbumMetadataJob(ctx context.Context) (moments.AlbumMetadataJob, bool, error) {
	job, err := scanAlbumMetadataJob(store.pool.QueryRow(ctx, `UPDATE album_metadata_jobs SET status = 'running',
		lease_token = $1, lease_until = now() + INTERVAL '15 minutes'
		WHERE id = (SELECT id FROM album_metadata_jobs WHERE scheduled_for <= now()
		AND (status = 'pending' OR (status = 'running' AND lease_until <= now()))
		ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, album_id, owner_id, status, result, error, lease_token`, uuid.NewString()))
	if errors.Is(err, moments.ErrNotFound) {
		return job, false, nil
	}
	return job, err == nil, err
}

func (store *Postgres) FinishAlbumMetadataJob(ctx context.Context, job moments.AlbumMetadataJob, metadata *moments.Metadata, message string, waiting bool) error {
	var result []byte
	var err error
	if metadata != nil {
		result, err = json.Marshal(metadata)
		if err != nil {
			return err
		}
	}
	status := "failed"
	if waiting {
		status = "pending"
	} else if metadata != nil {
		status = "completed"
	}
	changed, err := store.pool.Exec(ctx, `UPDATE album_metadata_jobs SET status = $3, result = $4, error = $5,
		lease_token = NULL, lease_until = NULL, scheduled_for = now() + INTERVAL '1 minute'
		WHERE id = $1 AND lease_token = $2 AND status = 'running'`, job.ID, job.LeaseToken, status, result, message)
	if err == nil && changed.RowsAffected() == 0 {
		return fmt.Errorf("album metadata job lease is no longer owned")
	}
	return err
}
