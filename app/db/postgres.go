package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (store *Postgres) Close() { store.pool.Close() }

func (store *Postgres) Ping(ctx context.Context) error { return store.pool.Ping(ctx) }

func (store *Postgres) Migrate(ctx context.Context) error {
	_, err := store.pool.Exec(ctx, schema)
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id UUID PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	totp_secret TEXT,
	role TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'user')),
	upload_folder TEXT,
	totp_required BOOLEAN NOT NULL DEFAULT FALSE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'admin';
ALTER TABLE users ADD COLUMN IF NOT EXISTS upload_folder TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_required BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS storage_quota_bytes BIGINT CHECK (storage_quota_bytes > 0);
ALTER TABLE users ADD COLUMN IF NOT EXISTS trash_retention_days INTEGER NOT NULL DEFAULT 30 CHECK (trash_retention_days BETWEEN 1 AND 3650);
UPDATE users SET totp_required = TRUE WHERE role = 'admin' AND totp_required = FALSE;
CREATE TABLE IF NOT EXISTS auth_challenges (
	token_hash TEXT PRIMARY KEY,
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	pending_totp_secret TEXT,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS auth_sessions (
	token_hash TEXT PRIMARY KEY,
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_sessions_expires_at_idx ON auth_sessions(expires_at);
CREATE TABLE IF NOT EXISTS upload_batches (
	id UUID PRIMARY KEY,
	owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL DEFAULT '',
	expected_files INTEGER NOT NULL CHECK (expected_files > 0),
	expected_bytes BIGINT NOT NULL CHECK (expected_bytes > 0),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'canceled')),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS totp_recovery_codes (
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	code_hash TEXT NOT NULL,
	used_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (user_id, code_hash)
);
CREATE TABLE IF NOT EXISTS media_uploads (
	upload_id TEXT PRIMARY KEY,
	owner_id UUID REFERENCES users(id) ON DELETE CASCADE,
	filename TEXT NOT NULL,
	mime_type TEXT NOT NULL,
	size BIGINT NOT NULL CHECK (size > 0),
	sha256 TEXT NOT NULL,
	media_path TEXT NOT NULL UNIQUE,
	created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS owner_id UUID REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS thumbnail_path TEXT;
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS width INTEGER;
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS height INTEGER;
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS video_duration_seconds DOUBLE PRECISION;
ALTER TABLE media_uploads ADD COLUMN IF NOT EXISTS batch_id UUID REFERENCES upload_batches(id) ON DELETE SET NULL;
UPDATE media_uploads SET owner_id = (
	SELECT id FROM users WHERE role = 'admin' ORDER BY created_at, id LIMIT 1
) WHERE owner_id IS NULL;
ALTER TABLE media_uploads ALTER COLUMN owner_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS media_uploads_owner_id_idx ON media_uploads(owner_id);
CREATE INDEX IF NOT EXISTS media_uploads_deleted_at_idx ON media_uploads(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS media_processing_jobs (
	id TEXT PRIMARY KEY,
	upload_id TEXT NOT NULL UNIQUE REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'processing', 'completed', 'failed')),
	attempts INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	scheduled_for TIMESTAMPTZ NOT NULL DEFAULT now(),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS media_processing_jobs_queue_idx ON media_processing_jobs(status, scheduled_for);
CREATE TABLE IF NOT EXISTS media_exif (
	upload_id TEXT PRIMARY KEY REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	captured_at TIMESTAMPTZ,
	latitude DOUBLE PRECISION,
	longitude DOUBLE PRECISION,
	raw_exif JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE IF NOT EXISTS media_shares (
	upload_id TEXT NOT NULL REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (upload_id, user_id)
);
CREATE TABLE IF NOT EXISTS user_media_shares (
	owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	permission TEXT NOT NULL DEFAULT 'read' CHECK (permission IN ('read', 'write')),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (owner_id, user_id),
	CHECK (owner_id <> user_id)
);
ALTER TABLE user_media_shares ADD COLUMN IF NOT EXISTS permission TEXT NOT NULL DEFAULT 'read';
ALTER TABLE user_media_shares DROP CONSTRAINT IF EXISTS user_media_shares_permission_check;
ALTER TABLE user_media_shares ADD CONSTRAINT user_media_shares_permission_check CHECK (permission IN ('read', 'write'));
INSERT INTO user_media_shares (owner_id, user_id)
	SELECT DISTINCT media.owner_id, share.user_id
	FROM media_shares share
	JOIN media_uploads media ON media.upload_id = share.upload_id
	WHERE media.owner_id IS NOT NULL AND media.owner_id <> share.user_id
	ON CONFLICT DO NOTHING;
DELETE FROM media_shares;
CREATE TABLE IF NOT EXISTS albums (
	id UUID PRIMARY KEY,
	owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
	description TEXT NOT NULL DEFAULT '',
	automatic BOOLEAN NOT NULL DEFAULT FALSE,
	auto_key TEXT,
	position INTEGER,
	cover_media_id TEXT REFERENCES media_uploads(upload_id) ON DELETE SET NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE albums ADD COLUMN IF NOT EXISTS automatic BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE albums ADD COLUMN IF NOT EXISTS auto_key TEXT;
ALTER TABLE albums ADD COLUMN IF NOT EXISTS position INTEGER;
ALTER TABLE albums ADD COLUMN IF NOT EXISTS cover_media_id TEXT REFERENCES media_uploads(upload_id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS albums_owner_position_idx ON albums(owner_id, position, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS albums_owner_auto_key_idx ON albums(owner_id, auto_key) WHERE auto_key IS NOT NULL;
CREATE TABLE IF NOT EXISTS import_sessions (
	id UUID PRIMARY KEY,
	owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	status TEXT NOT NULL CHECK (status IN ('processing', 'completed', 'failed')),
	imported_items INTEGER NOT NULL DEFAULT 0,
	error_message TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS public_shares (
	id TEXT PRIMARY KEY,
	owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	media_id TEXT REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	album_id UUID REFERENCES albums(id) ON DELETE CASCADE,
	password_hash TEXT,
	expires_at TIMESTAMPTZ NOT NULL,
	access_count BIGINT NOT NULL DEFAULT 0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK ((media_id IS NULL) <> (album_id IS NULL))
);
CREATE INDEX IF NOT EXISTS public_shares_expires_at_idx ON public_shares(expires_at);
CREATE TABLE IF NOT EXISTS album_media (
	album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
	upload_id TEXT NOT NULL REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (album_id, upload_id)
);
CREATE TABLE IF NOT EXISTS media_preferences (
	upload_id TEXT NOT NULL REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	favorite BOOLEAN NOT NULL DEFAULT FALSE,
	PRIMARY KEY (upload_id, user_id)
);
CREATE TABLE IF NOT EXISTS moments (
	id UUID PRIMARY KEY,
	owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
	description TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
	start_time TIMESTAMPTZ NOT NULL,
	end_time TIMESTAMPTZ NOT NULL,
	location_name TEXT NOT NULL DEFAULT '',
	image_count BIGINT NOT NULL DEFAULT 0 CHECK (image_count >= 0),
	cover_media_id TEXT REFERENCES media_uploads(upload_id) ON DELETE SET NULL,
	user_edited BOOLEAN NOT NULL DEFAULT FALSE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK (end_time >= start_time)
);
CREATE INDEX IF NOT EXISTS moments_owner_start_idx ON moments(owner_id, start_time DESC);
CREATE TABLE IF NOT EXISTS moment_media (
	moment_id UUID NOT NULL REFERENCES moments(id) ON DELETE CASCADE,
	upload_id TEXT NOT NULL REFERENCES media_uploads(upload_id) ON DELETE CASCADE,
	similarity_score DOUBLE PRECISION,
	representative_score DOUBLE PRECISION,
	is_representative BOOLEAN NOT NULL DEFAULT FALSE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (moment_id, upload_id)
);
CREATE INDEX IF NOT EXISTS moment_media_upload_idx ON moment_media(upload_id);
`
