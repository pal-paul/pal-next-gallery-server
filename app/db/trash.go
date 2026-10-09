package store

import (
	"context"
	"time"

	"pal-next-gallery-server/app/trash"
)

func (store *Postgres) ListExpiredTrash(ctx context.Context, now time.Time, limit int) ([]trash.ExpiredMedia, error) {
	rows, err := store.pool.Query(ctx, `SELECT media.upload_id, media.media_path, COALESCE(media.thumbnail_path, '')
		FROM media_uploads media JOIN users owner ON owner.id = media.owner_id
		WHERE media.deleted_at IS NOT NULL
		AND media.deleted_at + make_interval(days => owner.trash_retention_days) <= $1
		ORDER BY media.deleted_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []trash.ExpiredMedia
	for rows.Next() {
		var item trash.ExpiredMedia
		if err := rows.Scan(&item.ID, &item.MediaPath, &item.ThumbnailPath); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *Postgres) DeleteExpiredMedia(ctx context.Context, mediaID string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	affected, err := affectedMomentsForMedia(ctx, transaction, mediaID)
	if err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `DELETE FROM media_uploads WHERE upload_id = $1 AND deleted_at IS NOT NULL`, mediaID); err != nil {
		return err
	}
	if err := reconcileAffectedMoments(ctx, transaction, affected, true); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}
