package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (store *Postgres) UpsertTakeoutAlbum(ctx context.Context, ownerID, sourceKey, title string, mediaIDs []string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var albumID string
	err = transaction.QueryRow(ctx, `SELECT album_id FROM takeout_albums
		WHERE owner_id = $1 AND source_key = $2 FOR UPDATE`, ownerID, sourceKey).Scan(&albumID)
	if errors.Is(err, pgx.ErrNoRows) {
		albumID = uuid.NewString()
		if _, err := transaction.Exec(ctx, `INSERT INTO albums (id, owner_id, title, description)
			VALUES ($1, $2, $3, 'Imported from Google Photos')`, albumID, ownerID, title); err != nil {
			return err
		}
		if _, err := transaction.Exec(ctx, `INSERT INTO takeout_albums (owner_id, source_key, album_id)
			VALUES ($1, $2, $3)`, ownerID, sourceKey, albumID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if len(mediaIDs) > 0 {
		if _, err := transaction.Exec(ctx, `INSERT INTO album_media (album_id, upload_id)
			SELECT $1, media.upload_id FROM media_uploads media
			WHERE media.owner_id = $2 AND media.deleted_at IS NULL AND media.upload_id = ANY($3)
			ON CONFLICT DO NOTHING`, albumID, ownerID, mediaIDs); err != nil {
			return err
		}
	}
	return transaction.Commit(ctx)
}
