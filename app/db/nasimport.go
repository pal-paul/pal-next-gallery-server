package store

import (
	"context"

	"pal-next-gallery-server/app/nasimport"
)

func (store *Postgres) ListNASImportUsers(ctx context.Context) ([]nasimport.User, error) {
	rows, err := store.pool.Query(ctx, `SELECT id, upload_folder FROM users
		WHERE role = 'user' AND upload_folder IS NOT NULL AND btrim(upload_folder) <> ''
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]nasimport.User, 0)
	for rows.Next() {
		var user nasimport.User
		if err := rows.Scan(&user.ID, &user.UploadFolder); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}
