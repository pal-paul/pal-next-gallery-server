package store

import (
	"context"
	"fmt"
	"time"

	"pal-next-gallery-server/app/autoalbum"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (store *Postgres) ListAutomaticAlbumMedia(ctx context.Context) ([]autoalbum.Media, error) {
	rows, err := store.pool.Query(ctx, `WITH accessible_media AS (
		SELECT m.upload_id, m.owner_id AS user_id, COALESCE(exif.captured_at, m.created_at) AS album_date
		FROM media_uploads m
		LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id
		WHERE m.deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM media_processing_jobs job
			WHERE job.upload_id = m.upload_id AND job.status IN ('queued', 'processing'))
		UNION ALL
		SELECT m.upload_id, share.user_id, COALESCE(exif.captured_at, m.created_at) AS album_date
		FROM media_uploads m
		LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id
		JOIN user_media_shares share ON share.owner_id = m.owner_id
		WHERE m.deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM media_processing_jobs job
			WHERE job.upload_id = m.upload_id AND job.status IN ('queued', 'processing'))
	)
		SELECT accessible.upload_id, accessible.user_id, accessible.album_date
		FROM accessible_media accessible
		ORDER BY accessible.user_id, accessible.album_date, accessible.upload_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	media := make([]autoalbum.Media, 0)
	for rows.Next() {
		var item autoalbum.Media
		if err := rows.Scan(&item.ID, &item.AlbumOwnerID, &item.CreatedAt); err != nil {
			return nil, err
		}
		media = append(media, item)
	}
	return media, rows.Err()
}

func (store *Postgres) AssignAutomaticAlbum(ctx context.Context, assignment autoalbum.Assignment) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, assignment.OwnerID+":"+assignment.WeeklyKey); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, assignment.OwnerID+":"+assignment.DailyKey); err != nil {
		return err
	}

	var dailyCount int
	if err := transaction.QueryRow(ctx, `SELECT COUNT(*) FROM media_uploads m
		LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id
		WHERE m.deleted_at IS NULL AND (m.owner_id = $1 OR EXISTS (
			SELECT 1 FROM user_media_shares share
			WHERE share.owner_id = m.owner_id AND share.user_id = $1))
		AND NOT EXISTS (SELECT 1 FROM media_processing_jobs job WHERE job.upload_id = m.upload_id
			AND job.status IN ('queued', 'processing'))
		AND COALESCE(exif.captured_at, m.created_at) >= $2
		AND COALESCE(exif.captured_at, m.created_at) < $2 + INTERVAL '1 day'`,
		assignment.OwnerID, assignment.DayStart).Scan(&dailyCount); err != nil {
		return err
	}
	var dailyExists bool
	if err := transaction.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM albums
		WHERE owner_id = $1 AND automatic = TRUE
		AND (auto_key = $2 OR auto_key LIKE $2 || ':part:%'))`,
		assignment.OwnerID, assignment.DailyKey).Scan(&dailyExists); err != nil {
		return err
	}

	if dailyCount > assignment.DailyThreshold || dailyExists {
		dailyMedia, err := automaticMediaIDs(ctx, transaction, assignment.OwnerID, assignment.DayStart, assignment.DayStart.AddDate(0, 0, 1), false)
		if err != nil {
			return err
		}
		if err := reconcileAutomaticAlbumParts(ctx, transaction, assignment.OwnerID, assignment.DailyKey,
			assignment.DailyTitle, assignment.DailyAlbumID, dailyMedia, assignment.MaxAlbumMedia); err != nil {
			return err
		}
	}

	weeklyMedia, err := automaticMediaIDs(ctx, transaction, assignment.OwnerID, assignment.WeekStart,
		assignment.WeekEnd.AddDate(0, 0, 1), true)
	if err != nil {
		return err
	}
	if err := reconcileAutomaticAlbumParts(ctx, transaction, assignment.OwnerID, assignment.WeeklyKey,
		assignment.WeeklyTitle, assignment.WeeklyAlbumID, weeklyMedia, assignment.MaxAlbumMedia); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func automaticMediaIDs(ctx context.Context, transaction pgx.Tx, ownerID string, start, end time.Time, excludeDaily bool) ([]string, error) {
	rows, err := transaction.Query(ctx, `SELECT m.upload_id FROM media_uploads m
		LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id
		WHERE m.deleted_at IS NULL AND (m.owner_id = $1 OR EXISTS (
			SELECT 1 FROM user_media_shares share
			WHERE share.owner_id = m.owner_id AND share.user_id = $1))
		AND NOT EXISTS (SELECT 1 FROM media_processing_jobs job WHERE job.upload_id = m.upload_id
			AND job.status IN ('queued', 'processing'))
		AND COALESCE(exif.captured_at, m.created_at) >= $2
		AND COALESCE(exif.captured_at, m.created_at) < $3
		AND (NOT $4 OR NOT EXISTS (
			SELECT 1 FROM album_media am JOIN albums a ON a.id = am.album_id
			WHERE am.upload_id = m.upload_id AND a.owner_id = $1 AND a.automatic = TRUE
			AND a.auto_key LIKE 'day:%'))
		ORDER BY COALESCE(exif.captured_at, m.created_at), m.upload_id`, ownerID, start, end, excludeDaily)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mediaIDs := make([]string, 0)
	for rows.Next() {
		var mediaID string
		if err := rows.Scan(&mediaID); err != nil {
			return nil, err
		}
		mediaIDs = append(mediaIDs, mediaID)
	}
	return mediaIDs, rows.Err()
}

func reconcileAutomaticAlbumParts(ctx context.Context, transaction pgx.Tx, ownerID, baseKey, baseTitle, firstAlbumID string, mediaIDs []string, maxMedia int) error {
	if maxMedia <= 0 {
		return fmt.Errorf("maximum automatic album size must be positive")
	}
	rows, err := transaction.Query(ctx, `SELECT id FROM albums WHERE owner_id = $1 AND automatic = TRUE
		AND (auto_key = $2 OR auto_key LIKE $2 || ':part:%') FOR UPDATE`, ownerID, baseKey)
	if err != nil {
		return err
	}
	var albumIDs []string
	for rows.Next() {
		var albumID string
		if err := rows.Scan(&albumID); err != nil {
			rows.Close()
			return err
		}
		albumIDs = append(albumIDs, albumID)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	for _, albumID := range albumIDs {
		if _, err := transaction.Exec(ctx, `DELETE FROM album_media WHERE album_id = $1`, albumID); err != nil {
			return err
		}
	}

	for index, mediaPart := range partitionMediaIDs(mediaIDs, maxMedia) {
		part := index + 1
		key, title := automaticAlbumPart(baseKey, baseTitle, part)
		albumID := firstAlbumID
		if part > 1 {
			albumID = uuid.NewString()
		}
		if err := transaction.QueryRow(ctx, `INSERT INTO albums
			(id, owner_id, title, description, automatic, auto_key)
			VALUES ($1, $2, $3, '', TRUE, $4)
			ON CONFLICT (owner_id, auto_key) WHERE auto_key IS NOT NULL
			DO UPDATE SET title = EXCLUDED.title
			RETURNING id`, albumID, ownerID, title, key).Scan(&albumID); err != nil {
			return err
		}
		for _, mediaID := range mediaPart {
			if _, err := transaction.Exec(ctx, `INSERT INTO album_media (album_id, upload_id)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, albumID, mediaID); err != nil {
				return err
			}
		}
	}
	_, err = transaction.Exec(ctx, `DELETE FROM albums a WHERE a.owner_id = $1 AND a.automatic = TRUE
		AND (a.auto_key = $2 OR a.auto_key LIKE $2 || ':part:%')
		AND NOT EXISTS (SELECT 1 FROM album_media am WHERE am.album_id = a.id)`, ownerID, baseKey)
	return err
}

func automaticAlbumPart(baseKey, baseTitle string, part int) (string, string) {
	if part <= 1 {
		return baseKey, baseTitle
	}
	return fmt.Sprintf("%s:part:%d", baseKey, part), fmt.Sprintf("%s (Part %d)", baseTitle, part)
}

func partitionMediaIDs(mediaIDs []string, maxMedia int) [][]string {
	parts := make([][]string, 0, (len(mediaIDs)+maxMedia-1)/maxMedia)
	for offset := 0; offset < len(mediaIDs); offset += maxMedia {
		end := min(offset+maxMedia, len(mediaIDs))
		parts = append(parts, mediaIDs[offset:end])
	}
	return parts
}
