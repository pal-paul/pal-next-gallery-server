package store

import (
	"context"
	"errors"
	"strings"

	"pal-next-gallery-server/app/gallery"

	"github.com/jackc/pgx/v5"
)

func (store *Postgres) ListAlbums(ctx context.Context, userID, search string) ([]gallery.Album, error) {
	rows, err := store.pool.Query(ctx, `SELECT a.id, a.owner_id, a.title, a.description, a.automatic, a.created_at,
		COUNT(m.upload_id) FILTER (WHERE m.deleted_at IS NULL AND (m.owner_id = $1 OR s.user_id IS NOT NULL)),
		COALESCE(CASE WHEN BOOL_OR(m.upload_id = a.cover_media_id AND m.deleted_at IS NULL AND
			(m.owner_id = $1 OR s.user_id IS NOT NULL)) THEN a.cover_media_id END, '')
		FROM albums a
		LEFT JOIN album_media am ON am.album_id = a.id
		LEFT JOIN media_uploads m ON m.upload_id = am.upload_id
		LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $1
		WHERE a.owner_id = $1 AND ($2 = '' OR a.title ILIKE '%' || $2 || '%' OR
			a.description ILIKE '%' || $2 || '%' OR EXISTS (
				SELECT 1 FROM album_media search_am JOIN media_uploads search_m ON search_m.upload_id = search_am.upload_id
				LEFT JOIN user_media_shares search_s ON search_s.owner_id = search_m.owner_id AND search_s.user_id = $1
				WHERE search_am.album_id = a.id AND search_m.deleted_at IS NULL
				AND (search_m.owner_id = $1 OR search_s.user_id IS NOT NULL)
				AND search_m.filename ILIKE '%' || $2 || '%'))
		GROUP BY a.id
		ORDER BY a.position ASC NULLS LAST, a.created_at DESC`, userID, strings.TrimSpace(search))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	albums := make([]gallery.Album, 0)
	for rows.Next() {
		var album gallery.Album
		if err := rows.Scan(&album.ID, &album.OwnerID, &album.Title, &album.Description, &album.Automatic, &album.CreatedAt, &album.ItemCount, &album.CoverMediaID); err != nil {
			return nil, err
		}
		albums = append(albums, album)
	}
	return albums, rows.Err()
}

func (store *Postgres) CreateAlbum(ctx context.Context, album gallery.Album) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO albums (id, owner_id, title, description) VALUES ($1, $2, $3, $4)`,
		album.ID, album.OwnerID, album.Title, album.Description)
	return err
}

func (store *Postgres) GetAlbum(ctx context.Context, albumID, userID string) (gallery.Album, error) {
	var album gallery.Album
	err := store.pool.QueryRow(ctx, `SELECT id, owner_id, title, description, automatic, created_at, COALESCE(cover_media_id, '')
		FROM albums WHERE id = $1 AND owner_id = $2`, albumID, userID).Scan(
		&album.ID, &album.OwnerID, &album.Title, &album.Description, &album.Automatic, &album.CreatedAt, &album.CoverMediaID)
	if err != nil {
		return album, galleryError(err)
	}
	rows, err := store.pool.Query(ctx, mediaSelect+`
		JOIN album_media am ON am.upload_id = m.upload_id
		LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $2
		LEFT JOIN media_preferences p ON p.upload_id = m.upload_id AND p.user_id = $2
		WHERE am.album_id = $1 AND m.deleted_at IS NULL AND (m.owner_id = $2 OR s.user_id IS NOT NULL)
		ORDER BY m.created_at DESC`, albumID, userID)
	if err != nil {
		return album, err
	}
	defer rows.Close()
	album.Media, err = scanGalleryMedia(rows)
	album.ItemCount = int64(len(album.Media))
	return album, err
}

func (store *Postgres) UpdateAlbum(ctx context.Context, albumID, userID, title, description string) error {
	result, err := store.pool.Exec(ctx, `UPDATE albums SET title = $3, description = $4
		WHERE id = $1 AND owner_id = $2 AND automatic = FALSE`, albumID, userID, title, description)
	return changed(result.RowsAffected(), err)
}

func (store *Postgres) DeleteAlbum(ctx context.Context, albumID, userID string) error {
	result, err := store.pool.Exec(ctx, `DELETE FROM albums
		WHERE id = $1 AND owner_id = $2 AND automatic = FALSE`, albumID, userID)
	return changed(result.RowsAffected(), err)
}

func (store *Postgres) SetAlbumOrder(ctx context.Context, userID string, albumIDs []string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	for position, albumID := range albumIDs {
		result, err := transaction.Exec(ctx, `UPDATE albums SET position = $3
			WHERE id = $1 AND owner_id = $2 AND automatic = FALSE`, albumID, userID, position)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return gallery.ErrNotFound
		}
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) AddAlbumMedia(ctx context.Context, albumID, userID string, mediaIDs []string) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var albumExists bool
	if err := transaction.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM albums WHERE id = $1 AND owner_id = $2 AND automatic = FALSE)`, albumID, userID).Scan(&albumExists); err != nil {
		return err
	}
	if !albumExists {
		return gallery.ErrNotFound
	}
	uniqueIDs := make(map[string]struct{}, len(mediaIDs))
	for _, mediaID := range mediaIDs {
		uniqueIDs[mediaID] = struct{}{}
	}
	var accessibleCount int
	if err := transaction.QueryRow(ctx, `SELECT COUNT(DISTINCT m.upload_id) FROM media_uploads m
		LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $2
		WHERE m.upload_id = ANY($1) AND m.deleted_at IS NULL
		AND (m.owner_id = $2 OR s.user_id IS NOT NULL)`, mediaIDs, userID).Scan(&accessibleCount); err != nil {
		return err
	}
	if accessibleCount != len(uniqueIDs) {
		return gallery.ErrNotFound
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO album_media (album_id, upload_id)
		SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`, albumID, mediaIDs); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) RemoveAlbumMedia(ctx context.Context, albumID, userID, mediaID string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `DELETE FROM album_media am USING albums a
		WHERE am.album_id = a.id AND a.id = $1 AND a.owner_id = $2
		AND a.automatic = FALSE AND am.upload_id = $3`, albumID, userID, mediaID)
	if err := changed(result.RowsAffected(), err); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `UPDATE albums SET cover_media_id = NULL
		WHERE id = $1 AND owner_id = $2 AND cover_media_id = $3`, albumID, userID, mediaID); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) SetAlbumCover(ctx context.Context, albumID, userID, mediaID string) error {
	result, err := store.pool.Exec(ctx, `UPDATE albums a SET cover_media_id = $3
		WHERE a.id = $1 AND a.owner_id = $2 AND EXISTS (
			SELECT 1 FROM album_media am JOIN media_uploads m ON m.upload_id = am.upload_id
			LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $2
			WHERE am.album_id = a.id AND am.upload_id = $3 AND m.deleted_at IS NULL
			AND (m.owner_id = $2 OR s.user_id IS NOT NULL))`, albumID, userID, mediaID)
	return changed(result.RowsAffected(), err)
}

const mediaSelect = `SELECT m.upload_id, m.owner_id, owner.username,
	regexp_replace(m.filename, '\.[^.]+$', ''),
	CASE WHEN m.mime_type LIKE 'video/%' THEN 'video' ELSE 'photo' END,
	COALESCE(p.favorite, FALSE), m.filename, m.mime_type, m.size, m.sha256, m.created_at,
	m.deleted_at, m.owner_id <> $2, CASE WHEN m.owner_id = $2 THEN 'owner' ELSE s.permission END,
	COALESCE(m.thumbnail_path, ''), m.width, m.height,
	m.video_duration_seconds, exif.captured_at, exif.latitude, exif.longitude
	FROM media_uploads m JOIN users owner ON owner.id = m.owner_id
	LEFT JOIN media_exif exif ON exif.upload_id = m.upload_id `

func (store *Postgres) ListMedia(ctx context.Context, userID string, filter gallery.MediaFilter) ([]gallery.Media, error) {
	rows, err := store.pool.Query(ctx, mediaSelect+`
		LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $2
		LEFT JOIN media_preferences p ON p.upload_id = m.upload_id AND p.user_id = $2
		WHERE (($3 AND m.owner_id = $2 AND m.deleted_at IS NOT NULL) OR
			(NOT $3 AND m.deleted_at IS NULL AND (m.owner_id = $2 OR s.user_id IS NOT NULL)))
		AND ($4 = '' OR CASE WHEN m.mime_type LIKE 'video/%' THEN 'video' ELSE 'photo' END = $4)
		AND ($1 = '' OR m.filename ILIKE '%' || $1 || '%' OR m.mime_type ILIKE '%' || $1 || '%')
		AND ($6::timestamptz IS NULL OR exif.captured_at >= $6)
		AND ($7::timestamptz IS NULL OR exif.captured_at <= $7)
		AND ($8::double precision IS NULL OR $9::double precision IS NULL OR $10::double precision IS NULL OR
			(exif.latitude IS NOT NULL AND exif.longitude IS NOT NULL AND
			6371 * acos(LEAST(1, cos(radians($8)) * cos(radians(exif.latitude)) *
			cos(radians(exif.longitude) - radians($9)) + sin(radians($8)) * sin(radians(exif.latitude)))) <= $10))
		ORDER BY CASE WHEN $5 = 'oldest' THEN m.created_at END ASC,
			CASE WHEN $5 = 'title' THEN lower(m.filename) END ASC,
			CASE WHEN $5 NOT IN ('oldest', 'title') THEN m.created_at END DESC`,
		strings.TrimSpace(filter.Search), userID, filter.Trash, filter.Kind, filter.Sort,
		filter.CapturedAfter, filter.CapturedBefore, filter.Latitude, filter.Longitude, filter.RadiusKM)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGalleryMedia(rows)
}

func (store *Postgres) SetFavorite(ctx context.Context, mediaID, userID string, favorite bool) error {
	result, err := store.pool.Exec(ctx, `INSERT INTO media_preferences (upload_id, user_id, favorite)
		SELECT m.upload_id, $2, $3 FROM media_uploads m
		LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $2
		WHERE m.upload_id = $1 AND m.deleted_at IS NULL AND (m.owner_id = $2 OR s.user_id IS NOT NULL)
		ON CONFLICT (upload_id, user_id) DO UPDATE SET favorite = EXCLUDED.favorite`, mediaID, userID, favorite)
	return changed(result.RowsAffected(), err)
}

func (store *Postgres) TrashMedia(ctx context.Context, mediaID, userID string) error {
	result, err := store.pool.Exec(ctx, trashMediaQuery, mediaID, userID)
	return changed(result.RowsAffected(), err)
}

const trashMediaQuery = `UPDATE media_uploads media SET deleted_at = now()
		WHERE media.upload_id = $1 AND media.deleted_at IS NULL AND (
			media.owner_id = $2 OR EXISTS (
				SELECT 1 FROM user_media_shares share
				WHERE share.owner_id = media.owner_id AND share.user_id = $2 AND share.permission = 'write'
			)
		)`

func (store *Postgres) RestoreMedia(ctx context.Context, mediaID, userID string) error {
	result, err := store.pool.Exec(ctx, `UPDATE media_uploads SET deleted_at = NULL
		WHERE upload_id = $1 AND owner_id = $2 AND deleted_at IS NOT NULL`, mediaID, userID)
	return changed(result.RowsAffected(), err)
}

func (store *Postgres) GetOwnedMediaPaths(ctx context.Context, mediaID, userID string) (gallery.MediaPaths, error) {
	var paths gallery.MediaPaths
	err := store.pool.QueryRow(ctx, `SELECT media_path, COALESCE(thumbnail_path, '') FROM media_uploads
		WHERE upload_id = $1 AND owner_id = $2`, mediaID, userID).Scan(&paths.Media, &paths.Thumbnail)
	return paths, galleryError(err)
}

func (store *Postgres) DeleteMedia(ctx context.Context, mediaID, userID string) error {
	result, err := store.pool.Exec(ctx, `DELETE FROM media_uploads WHERE upload_id = $1 AND owner_id = $2`, mediaID, userID)
	return changed(result.RowsAffected(), err)
}

func (store *Postgres) GetStorage(ctx context.Context, userID string) (gallery.Storage, error) {
	var storage gallery.Storage
	err := store.pool.QueryRow(ctx, `SELECT COALESCE(SUM(m.size), 0),
		COALESCE(SUM(m.size) FILTER (WHERE m.mime_type NOT LIKE 'video/%'), 0),
		COALESCE(SUM(m.size) FILTER (WHERE m.mime_type LIKE 'video/%'), 0),
		COUNT(*) FILTER (WHERE m.mime_type NOT LIKE 'video/%'),
		COUNT(*) FILTER (WHERE m.mime_type LIKE 'video/%')
		FROM media_uploads m LEFT JOIN user_media_shares s ON s.owner_id = m.owner_id AND s.user_id = $1
		WHERE m.deleted_at IS NULL AND (m.owner_id = $1 OR s.user_id IS NOT NULL)`, userID).Scan(
		&storage.TotalBytes, &storage.PhotoBytes, &storage.VideoBytes, &storage.PhotoCount, &storage.VideoCount)
	return storage, err
}

type mediaRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanGalleryMedia(rows mediaRows) ([]gallery.Media, error) {
	media := make([]gallery.Media, 0)
	for rows.Next() {
		var item gallery.Media
		var thumbnailPath string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.OwnerUsername, &item.Title, &item.Kind,
			&item.Favorite, &item.Filename, &item.MimeType, &item.Size, &item.SHA256,
			&item.CreatedAt, &item.DeletedAt, &item.Shared, &item.Permission, &thumbnailPath, &item.Width, &item.Height,
			&item.Duration, &item.CapturedAt, &item.Latitude, &item.Longitude); err != nil {
			return nil, err
		}
		if thumbnailPath != "" {
			item.ThumbnailURL = "/media/files/" + item.ID + "/thumbnail"
		}
		media = append(media, item)
	}
	return media, rows.Err()
}

func changed(rowsAffected int64, err error) error {
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return gallery.ErrNotFound
	}
	return nil
}

func galleryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return gallery.ErrNotFound
	}
	return err
}
