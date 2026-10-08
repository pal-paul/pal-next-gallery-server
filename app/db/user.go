package store

import (
	"context"
	"errors"

	userapp "pal-next-gallery-server/app/user"
)

func (store *Postgres) CreateUser(ctx context.Context, account userapp.NewAccount) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO users
		(id, username, password_hash, totp_secret, role, upload_folder, totp_required)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), TRUE)`,
		account.ID, account.Username, account.PasswordHash, account.TOTPSecret, account.Role, account.UploadFolder)
	return err
}

func (store *Postgres) ListUsers(ctx context.Context) ([]userapp.Account, error) {
	rows, err := store.pool.Query(ctx, `SELECT users.id, users.username, users.role, COALESCE(users.upload_folder, ''),
		users.totp_secret IS NOT NULL, users.storage_quota_bytes, users.trash_retention_days,
		COALESCE(SUM(media.size) FILTER (WHERE media.deleted_at IS NULL), 0)
		FROM users LEFT JOIN media_uploads media ON media.owner_id = users.id
		GROUP BY users.id ORDER BY users.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []userapp.Account
	for rows.Next() {
		var account userapp.Account
		if err := rows.Scan(&account.ID, &account.Username, &account.Role, &account.UploadFolder, &account.TOTPEnabled,
			&account.StorageQuota, &account.TrashRetentionDays, &account.StorageUsed); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (store *Postgres) UpdateTrashRetention(ctx context.Context, userID string, days int) error {
	result, err := store.pool.Exec(ctx, `UPDATE users SET trash_retention_days = $2 WHERE id = $1`, userID, days)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	return err
}

func (store *Postgres) UpdateUserQuota(ctx context.Context, userID string, quota *int64) error {
	result, err := store.pool.Exec(ctx, `UPDATE users SET storage_quota_bytes = $2 WHERE id = $1`, userID, quota)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	return err
}

func (store *Postgres) UpdateUserFolder(ctx context.Context, userID, folder string) error {
	result, err := store.pool.Exec(ctx, `UPDATE users SET upload_folder = $2 WHERE id = $1 AND role = 'user'`, userID, folder)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("regular user not found")
	}
	return err
}

func (store *Postgres) ResetUserPassword(ctx context.Context, userID, passwordHash string) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	if _, err := transaction.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

func (store *Postgres) ListAccessibleMedia(ctx context.Context, userID string) ([]userapp.Media, error) {
	rows, err := store.pool.Query(ctx, `SELECT m.upload_id, m.owner_id, owner.username, m.filename, m.mime_type,
		m.size, m.sha256, m.media_path, m.created_at, m.owner_id <> $1,
		CASE WHEN m.owner_id = $1 THEN 'owner' ELSE (
			SELECT share.permission FROM user_media_shares share
			WHERE share.owner_id = m.owner_id AND share.user_id = $1
		) END
		FROM media_uploads m JOIN users owner ON owner.id = m.owner_id
		WHERE m.deleted_at IS NULL AND (m.owner_id = $1 OR EXISTS (
			SELECT 1 FROM user_media_shares share
			WHERE share.owner_id = m.owner_id AND share.user_id = $1
		)) ORDER BY m.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var media []userapp.Media
	for rows.Next() {
		var item userapp.Media
		if err := rows.Scan(&item.UploadID, &item.OwnerID, &item.OwnerUsername, &item.Filename, &item.MimeType,
			&item.Size, &item.SHA256, &item.MediaPath, &item.CreatedAt, &item.Shared, &item.Permission); err != nil {
			return nil, err
		}
		media = append(media, item)
	}
	return media, rows.Err()
}

func (store *Postgres) GetAccessibleMedia(ctx context.Context, uploadID, userID string) (userapp.Media, error) {
	var item userapp.Media
	err := store.pool.QueryRow(ctx, `SELECT m.upload_id, m.owner_id, owner.username, m.filename, m.mime_type,
		m.size, m.sha256, m.media_path, m.created_at, m.owner_id <> $2,
		CASE WHEN m.owner_id = $2 THEN 'owner' ELSE (
			SELECT share.permission FROM user_media_shares share
			WHERE share.owner_id = m.owner_id AND share.user_id = $2
		) END
		FROM media_uploads m JOIN users owner ON owner.id = m.owner_id
		WHERE m.upload_id = $1 AND m.deleted_at IS NULL AND (m.owner_id = $2 OR EXISTS (
			SELECT 1 FROM user_media_shares share
			WHERE share.owner_id = m.owner_id AND share.user_id = $2
		))`, uploadID, userID).Scan(&item.UploadID, &item.OwnerID, &item.OwnerUsername, &item.Filename,
		&item.MimeType, &item.Size, &item.SHA256, &item.MediaPath, &item.CreatedAt, &item.Shared, &item.Permission)
	return item, err
}

func (store *Postgres) GetAccessibleThumbnailPath(ctx context.Context, uploadID, userID string) (string, error) {
	var path string
	err := store.pool.QueryRow(ctx, `SELECT m.thumbnail_path FROM media_uploads m
		LEFT JOIN user_media_shares share ON share.owner_id = m.owner_id AND share.user_id = $2
		WHERE m.upload_id = $1 AND m.deleted_at IS NULL AND m.thumbnail_path IS NOT NULL
		AND (m.owner_id = $2 OR share.user_id IS NOT NULL)`, uploadID, userID).Scan(&path)
	return path, err
}

func (store *Postgres) ShareMedia(ctx context.Context, uploadID, ownerID, username, permission string) error {
	result, err := store.pool.Exec(ctx, `INSERT INTO user_media_shares (owner_id, user_id, permission)
		SELECT media.owner_id, target.id, $4 FROM media_uploads media JOIN users target ON target.username = $3
		WHERE media.upload_id = $1 AND media.owner_id = $2 AND media.deleted_at IS NULL
		AND target.role = 'user' AND target.id <> $2
		ON CONFLICT (owner_id, user_id) DO UPDATE SET permission = EXCLUDED.permission`, uploadID, ownerID, username, permission)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("owned media or target user not found")
	}
	return err
}

func (store *Postgres) UnshareMedia(ctx context.Context, uploadID, ownerID, username string) error {
	result, err := store.pool.Exec(ctx, `DELETE FROM user_media_shares share USING media_uploads media, users target
		WHERE media.upload_id = $1 AND media.owner_id = $2 AND share.owner_id = media.owner_id
		AND share.user_id = target.id AND target.username = $3`, uploadID, ownerID, username)
	if err == nil && result.RowsAffected() == 0 {
		return errors.New("library share not found")
	}
	return err
}
