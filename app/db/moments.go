package store

import (
	"context"
	"errors"

	"pal-next-gallery-server/app/moments"

	"github.com/jackc/pgx/v5"
)

const momentSelect = `SELECT moment.id, moment.owner_id, moment.title, moment.description, moment.status,
	moment.start_time, moment.end_time, moment.location_name, moment.image_count,
	COALESCE(moment.cover_media_id, ''), moment.user_edited, moment.created_at, moment.updated_at`

func (store *Postgres) ListMomentOwners(ctx context.Context) ([]string, error) {
	rows, err := store.pool.Query(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ownerIDs := make([]string, 0)
	for rows.Next() {
		var ownerID string
		if err := rows.Scan(&ownerID); err != nil {
			return nil, err
		}
		ownerIDs = append(ownerIDs, ownerID)
	}
	return ownerIDs, rows.Err()
}

func (store *Postgres) ListMoments(ctx context.Context, ownerID string) ([]moments.Moment, error) {
	rows, err := store.pool.Query(ctx, momentSelect+`
		FROM moments moment
		WHERE moment.owner_id = $1
		ORDER BY moment.start_time DESC, moment.id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]moments.Moment, 0)
	for rows.Next() {
		item, err := scanMoment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *Postgres) GetMoment(ctx context.Context, momentID, ownerID string) (moments.Moment, error) {
	item, err := scanMoment(store.pool.QueryRow(ctx, momentSelect+`
		FROM moments moment WHERE moment.id = $1 AND moment.owner_id = $2`, momentID, ownerID))
	if err != nil {
		return item, momentError(err)
	}
	rows, err := store.pool.Query(ctx, `SELECT media.upload_id, media.filename,
		CASE WHEN media.thumbnail_path IS NULL THEN '' ELSE '/media/files/' || media.upload_id || '/thumbnail' END,
		COALESCE(exif.captured_at, media.created_at), member.is_representative
		FROM moment_media member
		JOIN media_uploads media ON media.upload_id = member.upload_id
		LEFT JOIN media_exif exif ON exif.upload_id = media.upload_id
		WHERE member.moment_id = $1 AND media.deleted_at IS NULL
		ORDER BY COALESCE(exif.captured_at, media.created_at), media.upload_id`, momentID)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	item.Media = make([]moments.Media, 0)
	for rows.Next() {
		var media moments.Media
		if err := rows.Scan(&media.ID, &media.Filename, &media.ThumbnailURL, &media.CapturedAt, &media.Representative); err != nil {
			return item, err
		}
		item.Media = append(item.Media, media)
	}
	return item, rows.Err()
}

func (store *Postgres) UpdateMoment(ctx context.Context, momentID, ownerID, title, description, status string) error {
	result, err := store.pool.Exec(ctx, `UPDATE moments
		SET title = $3, description = $4, status = $5, user_edited = TRUE, updated_at = now()
		WHERE id = $1 AND owner_id = $2`, momentID, ownerID, title, description, status)
	return momentChanged(result.RowsAffected(), err)
}

func (store *Postgres) DeleteMoment(ctx context.Context, momentID, ownerID string) error {
	result, err := store.pool.Exec(ctx, `DELETE FROM moments WHERE id = $1 AND owner_id = $2`, momentID, ownerID)
	return momentChanged(result.RowsAffected(), err)
}

func (store *Postgres) AddMomentMedia(ctx context.Context, momentID, ownerID string, mediaIDs []string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var exists bool
	if err := transaction.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM moments WHERE id = $1 AND owner_id = $2)`, momentID, ownerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return moments.ErrNotFound
	}
	uniqueIDs := make(map[string]struct{}, len(mediaIDs))
	for _, mediaID := range mediaIDs {
		uniqueIDs[mediaID] = struct{}{}
	}
	var accessibleCount int
	if err := transaction.QueryRow(ctx, `SELECT COUNT(DISTINCT media.upload_id) FROM media_uploads media
		LEFT JOIN user_media_shares share ON share.owner_id = media.owner_id AND share.user_id = $2
		WHERE media.upload_id = ANY($1) AND media.deleted_at IS NULL
		AND media.mime_type LIKE 'image/%' AND (media.owner_id = $2 OR share.user_id IS NOT NULL)`, mediaIDs, ownerID).Scan(&accessibleCount); err != nil {
		return err
	}
	if accessibleCount != len(uniqueIDs) {
		return moments.ErrNotFound
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO moment_media (moment_id, upload_id)
		SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`, momentID, mediaIDs); err != nil {
		return err
	}
	if err := refreshMoment(ctx, transaction, momentID, ownerID); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) RemoveMomentMedia(ctx context.Context, momentID, ownerID, mediaID string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `DELETE FROM moment_media member USING moments moment
		WHERE member.moment_id = moment.id AND moment.id = $1 AND moment.owner_id = $2
		AND member.upload_id = $3 AND (SELECT COUNT(*) FROM moment_media WHERE moment_id = $1) > 1`,
		momentID, ownerID, mediaID)
	if err := momentChanged(result.RowsAffected(), err); err != nil {
		return err
	}
	if err := refreshMoment(ctx, transaction, momentID, ownerID); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) SetMomentCover(ctx context.Context, momentID, ownerID, mediaID string) error {
	result, err := store.pool.Exec(ctx, `UPDATE moments moment
		SET cover_media_id = $3, user_edited = TRUE, updated_at = now()
		WHERE moment.id = $1 AND moment.owner_id = $2 AND EXISTS (
			SELECT 1 FROM moment_media member WHERE member.moment_id = moment.id AND member.upload_id = $3)`,
		momentID, ownerID, mediaID)
	return momentChanged(result.RowsAffected(), err)
}

func (store *Postgres) ListMomentCandidates(ctx context.Context, ownerID string) ([]moments.Candidate, error) {
	rows, err := store.pool.Query(ctx, `SELECT media.upload_id, COALESCE(media.thumbnail_path, media.media_path),
		COALESCE(exif.captured_at, media.created_at),
		exif.latitude, exif.longitude, COALESCE(embedding.embedding, '{}'::DOUBLE PRECISION[])
		FROM media_uploads media
		LEFT JOIN media_exif exif ON exif.upload_id = media.upload_id
		LEFT JOIN LATERAL (SELECT stored.embedding FROM media_embeddings stored
			WHERE stored.upload_id = media.upload_id ORDER BY stored.created_at DESC LIMIT 1) embedding ON TRUE
		LEFT JOIN user_media_shares share ON share.owner_id = media.owner_id AND share.user_id = $1
		WHERE media.deleted_at IS NULL AND media.mime_type LIKE 'image/%'
		AND (media.owner_id = $1 OR share.user_id IS NOT NULL)
		AND COALESCE(exif.captured_at, media.created_at) >= now() - INTERVAL '7 days'
		AND COALESCE(exif.captured_at, media.created_at) <= now()
		AND EXISTS (SELECT 1 FROM media_processing_jobs job
			WHERE job.upload_id = media.upload_id AND job.status = 'completed')
		AND NOT EXISTS (SELECT 1 FROM media_duplicate_group_members duplicate
			WHERE duplicate.upload_id = media.upload_id AND duplicate.is_primary = FALSE)
		AND NOT EXISTS (SELECT 1 FROM moment_media member JOIN moments moment ON moment.id = member.moment_id
			WHERE member.upload_id = media.upload_id AND moment.owner_id = $1)
		ORDER BY COALESCE(exif.captured_at, media.created_at), media.upload_id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]moments.Candidate, 0)
	for rows.Next() {
		var candidate moments.Candidate
		if err := rows.Scan(&candidate.ID, &candidate.SourcePath, &candidate.CapturedAt, &candidate.Latitude,
			&candidate.Longitude, &candidate.Embedding); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (store *Postgres) ListImageDescriptions(ctx context.Context, mediaIDs []string) ([]moments.ImageDescription, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	rows, err := store.pool.Query(ctx, `SELECT DISTINCT ON (description.upload_id)
		description.upload_id, description.model, description.version, description.people,
		description.activities, description.location_type, description.objects, description.scene,
		description.weather, description.description
		FROM media_image_descriptions description
		WHERE description.upload_id = ANY($1)
		ORDER BY description.upload_id, description.created_at DESC`, mediaIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	descriptions := make([]moments.ImageDescription, 0, len(mediaIDs))
	for rows.Next() {
		var description moments.ImageDescription
		if err := rows.Scan(&description.MediaID, &description.Model, &description.Version, &description.People,
			&description.Activities, &description.LocationType, &description.Objects, &description.Scene,
			&description.Weather, &description.Description); err != nil {
			return nil, err
		}
		descriptions = append(descriptions, description)
	}
	return descriptions, rows.Err()
}

func (store *Postgres) SaveImageDescriptions(ctx context.Context, descriptions []moments.ImageDescription) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	for _, description := range descriptions {
		if _, err := transaction.Exec(ctx, `INSERT INTO media_image_descriptions
			(upload_id, model, version, people, activities, location_type, objects, scene, weather, description)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (upload_id, model, version) DO UPDATE SET people = EXCLUDED.people,
			activities = EXCLUDED.activities, location_type = EXCLUDED.location_type, objects = EXCLUDED.objects,
			scene = EXCLUDED.scene, weather = EXCLUDED.weather, description = EXCLUDED.description, created_at = now()`,
			description.MediaID, description.Model, description.Version, description.People, description.Activities,
			description.LocationType, description.Objects, description.Scene, description.Weather, description.Description); err != nil {
			return err
		}
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) CreateGeneratedMoment(ctx context.Context, moment moments.Moment, candidates []moments.Candidate) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "moments:"+moment.OwnerID); err != nil {
		return err
	}
	mediaIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		mediaIDs = append(mediaIDs, candidate.ID)
	}
	var availableCount int
	if err := transaction.QueryRow(ctx, `SELECT COUNT(*) FROM media_uploads media
		LEFT JOIN user_media_shares share ON share.owner_id = media.owner_id AND share.user_id = $2
		WHERE media.upload_id = ANY($1) AND media.deleted_at IS NULL AND media.mime_type LIKE 'image/%'
		AND (media.owner_id = $2 OR share.user_id IS NOT NULL)
		AND NOT EXISTS (SELECT 1 FROM moment_media member JOIN moments existing ON existing.id = member.moment_id
			WHERE member.upload_id = media.upload_id AND existing.owner_id = $2)`, mediaIDs, moment.OwnerID).Scan(&availableCount); err != nil {
		return err
	}
	if availableCount != len(mediaIDs) {
		return moments.ErrCandidatesAssigned
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO moments
		(id, owner_id, title, description, status, start_time, end_time, image_count, cover_media_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, moment.ID, moment.OwnerID, moment.Title,
		moment.Description, moment.Status, moment.StartTime, moment.EndTime, len(mediaIDs), moment.CoverMediaID); err != nil {
		return err
	}
	for _, candidate := range candidates {
		if _, err := transaction.Exec(ctx, `INSERT INTO moment_media
			(moment_id, upload_id, similarity_score, representative_score, is_representative)
			VALUES ($1, $2, $3, $4, $5)`, moment.ID, candidate.ID, candidate.SimilarityScore,
			candidate.RepresentativeScore, candidate.Representative); err != nil {
			return err
		}
	}
	return transaction.Commit(ctx)
}

func refreshMoment(ctx context.Context, transaction pgx.Tx, momentID, ownerID string) error {
	result, err := transaction.Exec(ctx, `UPDATE moments moment SET
		start_time = aggregate.start_time, end_time = aggregate.end_time,
		image_count = aggregate.image_count,
		cover_media_id = CASE WHEN moment.cover_media_id = ANY(aggregate.media_ids)
			THEN moment.cover_media_id ELSE aggregate.media_ids[1] END,
		user_edited = TRUE, updated_at = now()
		FROM (SELECT MIN(COALESCE(exif.captured_at, media.created_at)) AS start_time,
			MAX(COALESCE(exif.captured_at, media.created_at)) AS end_time,
			COUNT(*) AS image_count,
			ARRAY_AGG(media.upload_id ORDER BY COALESCE(exif.captured_at, media.created_at), media.upload_id) AS media_ids
			FROM moment_media member JOIN media_uploads media ON media.upload_id = member.upload_id
			LEFT JOIN media_exif exif ON exif.upload_id = media.upload_id WHERE member.moment_id = $1) aggregate
		WHERE moment.id = $1 AND moment.owner_id = $2`, momentID, ownerID)
	return momentChanged(result.RowsAffected(), err)
}

type momentRow interface{ Scan(...any) error }

func scanMoment(row momentRow) (moments.Moment, error) {
	var item moments.Moment
	err := row.Scan(&item.ID, &item.OwnerID, &item.Title, &item.Description, &item.Status,
		&item.StartTime, &item.EndTime, &item.LocationName, &item.ImageCount, &item.CoverMediaID,
		&item.UserEdited, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func momentChanged(rowsAffected int64, err error) error {
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return moments.ErrNotFound
	}
	return nil
}

func momentError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return moments.ErrNotFound
	}
	return err
}
