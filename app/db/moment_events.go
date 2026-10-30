package store

import (
	"context"

	"pal-next-gallery-server/app/moments"
)

func (store *Postgres) ListEventPhotos(ctx context.Context, job moments.DescriptionJob) ([]moments.EventPhoto, error) {
	rows, err := store.pool.Query(ctx, `WITH available AS (
		SELECT media.upload_id, COALESCE(media.thumbnail_path, media.media_path) AS source_path,
		COALESCE(exif.captured_at, media.created_at) AS captured_at, exif.captured_at IS NOT NULL AS reliable_time,
		exif.latitude, exif.longitude, COALESCE(member.moment_id::text, '') AS moment_id,
		COALESCE('moment:' || member.moment_id::text, 'photo:' || media.upload_id) AS group_id,
		COALESCE(description.model, '') AS model, COALESCE(description.version, '') AS version,
		COALESCE(description.description, '') AS description, COALESCE(description.activities, '{}') AS activities,
		COALESCE(description.objects, '{}') AS objects, COALESCE(description.scene, '') AS scene,
		COALESCE(description.location_type, '') AS location_type, COALESCE(description.weather, '') AS weather,
		COALESCE(description.people, '{}') AS people, COALESCE(description.keywords, '{}') AS keywords,
		COALESCE(description.text_embedding, '{}') AS text_embedding,
		COALESCE(description.text_embedding_model, '') AS text_embedding_model
		FROM media_uploads media
		LEFT JOIN media_exif exif ON exif.upload_id = media.upload_id
		LEFT JOIN LATERAL (SELECT membership.moment_id FROM moment_media membership
			JOIN moments moment ON moment.id = membership.moment_id
			WHERE membership.upload_id = media.upload_id AND moment.owner_id = $1 LIMIT 1) member ON TRUE
		LEFT JOIN LATERAL (SELECT stored.* FROM media_image_descriptions stored
			WHERE stored.upload_id = media.upload_id ORDER BY stored.created_at DESC LIMIT 1) description ON TRUE
		WHERE media.owner_id = $1 AND media.deleted_at IS NULL AND media.moments_enabled
		AND media.mime_type LIKE 'image/%'
		AND (description.upload_id IS NOT NULL OR member.moment_id IS NOT NULL)
		AND NOT EXISTS (SELECT 1 FROM media_duplicate_group_members duplicate
			WHERE duplicate.upload_id = media.upload_id AND NOT duplicate.is_primary)
	), seed AS (SELECT * FROM available WHERE upload_id = $2), metrics AS (
		SELECT candidate.group_id,
		MIN(CASE WHEN candidate.reliable_time AND seed.reliable_time THEN
			ABS(EXTRACT(EPOCH FROM candidate.captured_at - seed.captured_at)) END) AS time_gap,
		MAX(candidate.captured_at) AS recency,
		BOOL_OR((candidate.reliable_time AND seed.reliable_time AND
			ABS(EXTRACT(EPOCH FROM candidate.captured_at - seed.captured_at)) <= 172800) OR
			(candidate.latitude BETWEEN -90 AND 90 AND seed.latitude BETWEEN -90 AND 90 AND
			candidate.longitude BETWEEN -180 AND 180 AND seed.longitude BETWEEN -180 AND 180 AND
			ABS(candidate.latitude - seed.latitude) <= 50.0 / 111.0)) AS metadata_match,
		MAX(CASE WHEN candidate.text_embedding_model <> '' AND candidate.text_embedding_model = seed.text_embedding_model
			THEN gallery_text_cosine(candidate.text_embedding, seed.text_embedding) END) AS semantic_score
		FROM available candidate CROSS JOIN seed WHERE candidate.group_id <> seed.group_id GROUP BY candidate.group_id
	), metadata_ranked AS (
		SELECT group_id, row_number() OVER (ORDER BY time_gap NULLS LAST, recency DESC, group_id) AS rank
		FROM metrics WHERE metadata_match
	), semantic_ranked AS (
		SELECT group_id, row_number() OVER (ORDER BY semantic_score DESC, group_id) AS rank
		FROM metrics WHERE semantic_score > 0
	), ranked AS (SELECT * FROM metadata_ranked UNION ALL SELECT * FROM semantic_ranked), selected AS (
		SELECT group_id FROM ranked GROUP BY group_id ORDER BY MIN(rank), group_id LIMIT 50
	)
	SELECT upload_id, source_path, captured_at, reliable_time, latitude, longitude, moment_id,
	model, version, description, activities, objects, scene, location_type, weather, people,
	keywords, text_embedding, text_embedding_model FROM available
	WHERE group_id IN (SELECT group_id FROM selected) OR group_id IN (SELECT group_id FROM seed)
	ORDER BY captured_at, upload_id`, job.OwnerID, job.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var photos []moments.EventPhoto
	for rows.Next() {
		var photo moments.EventPhoto
		if err := rows.Scan(&photo.ID, &photo.SourcePath, &photo.CapturedAt, &photo.ReliableCapturedAt,
			&photo.Latitude, &photo.Longitude, &photo.MomentID, &photo.Description.Model, &photo.Description.Version,
			&photo.Description.Description, &photo.Description.Activities, &photo.Description.Objects, &photo.Description.Scene,
			&photo.Description.LocationType, &photo.Description.Weather, &photo.Description.People,
			&photo.Description.Keywords, &photo.Description.Embedding, &photo.Description.EmbeddingModel); err != nil {
			return nil, err
		}
		photo.Description.MediaID = photo.ID
		photos = append(photos, photo)
	}
	return photos, rows.Err()
}

func (store *Postgres) SaveEventGroup(ctx context.Context, moment moments.Moment, candidates []moments.Candidate, isNew bool) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "moments:"+moment.OwnerID); err != nil {
		return err
	}
	mediaIDs := make([]string, len(candidates))
	for index, candidate := range candidates {
		mediaIDs[index] = candidate.ID
	}
	var available int
	if err := transaction.QueryRow(ctx, `SELECT COUNT(*) FROM media_uploads media
		WHERE media.upload_id = ANY($1) AND media.owner_id = $2 AND media.deleted_at IS NULL
		AND media.moments_enabled AND media.mime_type LIKE 'image/%'
		AND NOT EXISTS (SELECT 1 FROM moment_media member JOIN moments existing ON existing.id = member.moment_id
			WHERE member.upload_id = media.upload_id AND existing.owner_id = $2)
		AND NOT EXISTS (SELECT 1 FROM media_duplicate_group_members duplicate
			WHERE duplicate.upload_id = media.upload_id AND NOT duplicate.is_primary)`, mediaIDs, moment.OwnerID).Scan(&available); err != nil {
		return err
	}
	if available != len(mediaIDs) {
		return moments.ErrCandidatesAssigned
	}
	if isNew {
		if len(mediaIDs) < 5 {
			return moments.ErrCandidatesAssigned
		}
		if _, err := transaction.Exec(ctx, `INSERT INTO moments
			(id, owner_id, title, description, status, start_time, end_time, image_count, cover_media_id)
			VALUES ($1, $2, $3, $4, 'draft', $5, $6, $7, $8)`, moment.ID, moment.OwnerID, moment.Title,
			moment.Description, moment.StartTime, moment.EndTime, len(mediaIDs), moment.CoverMediaID); err != nil {
			return err
		}
	} else {
		var exists bool
		if err := transaction.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM moments WHERE id = $1 AND owner_id = $2)`, moment.ID, moment.OwnerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return moments.ErrNotFound
		}
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO moment_media (moment_id, upload_id)
		SELECT $1::uuid, unnest($2::text[])`, moment.ID, mediaIDs); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `UPDATE moments SET
		start_time = LEAST(start_time, $3), end_time = GREATEST(end_time, $4),
		image_count = (SELECT COUNT(*) FROM moment_media WHERE moment_id = $1), updated_at = now()
		WHERE id = $1 AND owner_id = $2`, moment.ID, moment.OwnerID, moment.StartTime, moment.EndTime); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}
