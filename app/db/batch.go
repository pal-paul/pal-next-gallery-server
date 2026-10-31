package store

import (
	"context"
	"errors"

	"pal-next-gallery-server/app/batch"
)

func (store *Postgres) CreateUploadBatch(ctx context.Context, item batch.Batch) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO upload_batches
		(id, owner_id, name, expected_files, expected_bytes, status, created_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6)`, item.ID, item.OwnerID, item.Name, item.ExpectedFiles, item.ExpectedBytes, item.CreatedAt)
	return err
}

func (store *Postgres) GetUploadBatch(ctx context.Context, id, ownerID string) (batch.Batch, error) {
	var item batch.Batch
	err := store.pool.QueryRow(ctx, `SELECT batch.id, batch.owner_id, batch.name, batch.expected_files,
		batch.expected_bytes, COUNT(media.upload_id), COALESCE(SUM(media.size), 0),
		CASE WHEN batch.status = 'active' AND COUNT(media.upload_id) >= batch.expected_files THEN 'completed' ELSE batch.status END,
		batch.created_at FROM upload_batches batch LEFT JOIN media_uploads media ON media.batch_id = batch.id
		WHERE batch.id = $1 AND batch.owner_id = $2 GROUP BY batch.id`, id, ownerID).Scan(
		&item.ID, &item.OwnerID, &item.Name, &item.ExpectedFiles, &item.ExpectedBytes, &item.CompletedFiles,
		&item.CompletedBytes, &item.Status, &item.CreatedAt)
	return item, err
}

func (store *Postgres) CancelUploadBatch(ctx context.Context, id, ownerID string) error {
	result, err := store.pool.Exec(ctx, `UPDATE upload_batches SET status = 'canceled'
		WHERE id = $1 AND owner_id = $2 AND status IN ('active', 'canceled')`, id, ownerID)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("active upload batch not found")
	}
	return err
}
