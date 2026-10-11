package store

import (
	"context"
	"errors"

	"pal-next-gallery-server/app/uploader"

	"github.com/jackc/pgx/v5"
)

func (store *Postgres) FindOwnedMediaBySHA256(ctx context.Context, ownerID, checksum string) (uploader.CompletedMedia, bool, error) {
	var media uploader.CompletedMedia
	err := store.pool.QueryRow(ctx, `SELECT upload_id, owner_id, filename, mime_type, size, sha256, media_path, created_at
		FROM media_uploads WHERE owner_id = $1 AND sha256 = $2 AND deleted_at IS NULL
		ORDER BY created_at LIMIT 1`, ownerID, checksum).Scan(
		&media.UploadID, &media.OwnerID, &media.Filename, &media.MimeType, &media.Size, &media.SHA256, &media.MediaPath, &media.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uploader.CompletedMedia{}, false, nil
	}
	return media, err == nil, err
}

func (store *Postgres) OwnerStorageBytes(ctx context.Context, ownerID string) (int64, error) {
	var size int64
	err := store.pool.QueryRow(ctx, `SELECT COALESCE(SUM(size), 0) FROM media_uploads WHERE owner_id = $1`, ownerID).Scan(&size)
	return size, err
}

func (store *Postgres) OwnerStorageQuota(ctx context.Context, ownerID string) (int64, bool, error) {
	var quota *int64
	err := store.pool.QueryRow(ctx, `SELECT storage_quota_bytes FROM users WHERE id = $1`, ownerID).Scan(&quota)
	if err != nil || quota == nil {
		return 0, false, err
	}
	return *quota, true, nil
}

func (store *Postgres) ValidateUploadBatch(ctx context.Context, batchID, ownerID string) error {
	var valid bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upload_batches
		WHERE id = $1 AND owner_id = $2 AND status = 'active')`, batchID, ownerID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("active upload batch not found")
	}
	return nil
}

func (store *Postgres) ListMediaPaths(ctx context.Context) ([]string, error) {
	rows, err := store.pool.Query(ctx, `SELECT media_path FROM media_uploads ORDER BY media_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (store *Postgres) SaveCompletedMedia(ctx context.Context, media uploader.CompletedMedia) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, media.OwnerID+":"+media.SHA256); err != nil {
		return err
	}
	var duplicate bool
	if err := transaction.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_uploads
		WHERE owner_id = $1 AND sha256 = $2 AND upload_id <> $3 AND deleted_at IS NULL)`,
		media.OwnerID, media.SHA256, media.UploadID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return uploader.ErrDuplicateMedia
	}
	if media.BatchID != "" {
		var active bool
		if err := transaction.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM upload_batches
			WHERE id = $1 AND owner_id = $2 AND status = 'active')`, media.BatchID, media.OwnerID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return errors.New("upload batch is no longer active")
		}
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO media_uploads
		(upload_id, owner_id, filename, mime_type, size, sha256, media_path, batch_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid, $9)
		ON CONFLICT (upload_id) DO UPDATE SET
			owner_id = EXCLUDED.owner_id,
			filename = EXCLUDED.filename,
			mime_type = EXCLUDED.mime_type,
			size = EXCLUDED.size,
			sha256 = EXCLUDED.sha256,
			media_path = EXCLUDED.media_path,
			batch_id = EXCLUDED.batch_id,
			created_at = EXCLUDED.created_at`,
		media.UploadID, media.OwnerID, media.Filename, media.MimeType, media.Size, media.SHA256, media.MediaPath, media.BatchID, media.CreatedAt); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO media_processing_jobs (id, upload_id)
		VALUES ($1, $1) ON CONFLICT (upload_id) DO NOTHING`, media.UploadID); err != nil {
		return err
	}
	if media.CapturedAt != nil {
		if _, err := transaction.Exec(ctx, `INSERT INTO media_exif (upload_id, captured_at)
			VALUES ($1, $2) ON CONFLICT (upload_id) DO UPDATE SET captured_at = EXCLUDED.captured_at`,
			media.UploadID, media.CapturedAt); err != nil {
			return err
		}
	}
	return transaction.Commit(ctx)
}
