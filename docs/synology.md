# Synology deployment and operations

## Prepare directories

Create these directories from DSM File Station or SSH and restrict them to the
account used by Container Manager:

```text
/volume1/docker/next-gallery-server/postgres
/volume1/docker/next-gallery-server/backups
/volume1/media/gallery
/volume1/media/import
/volume1/media/tmp
```

Edit `build/compose.synology.yaml` and replace every `<change-me>` value. The
PostgreSQL username and password in `ENV_DATABASE_URL` must match the values on
the `postgres` service. Also set the external HTTPS origin and trusted proxy if
DSM reverse proxy is used.

The Synology Compose file contains all settings and does not require a `.env`
file. Deploy it as a Container Manager project, or from SSH:

```sh
docker compose -f build/compose.synology.yaml pull
docker compose -f build/compose.synology.yaml up -d
```

The server scans `NAS_IMPORT_PATH` nightly at `ENV_AUTO_ALBUM_RUN_AT`. In a
single-user installation, place media directly in `/volume1/media/import` or
any directory below it. In a multi-user installation, place each user's media
below a top-level directory matching that user's configured `upload_folder`,
for example `/volume1/media/import/alice/phone/photo.jpg`. Successfully imported
files are moved into the gallery storage tree and removed from the import tree.
Files that cannot be imported remain in place for the next run. Metadata
processing runs before imported media becomes eligible for automatic albums,
which group media by capture date when that metadata is available.

Images are published for `linux/amd64` and `linux/arm64`. The application image
includes FFmpeg for later thumbnail and preview processing and runs as a
non-root user.

## Reverse proxy

Use DSM **Login Portal > Advanced > Reverse Proxy** to terminate HTTPS and
forward to the media server port. Set `ENV_CORS_ALLOWED_ORIGINS` to the external
HTTPS origin. Set `ENV_TRUSTED_PROXIES` to the source address or CIDR from which
DSM reaches the container. This value controls whether forwarded client IP and
HTTPS headers are trusted; leave it empty when no reverse proxy is used.

After changing the Compose environment, recreate the application container
(Compose service `server`). Confirm the session cookie is marked `Secure` through the HTTPS
endpoint.

## Monitoring

- `GET /healthz` checks that the HTTP process is alive.
- `GET /readyz` checks PostgreSQL plus the configured free-space reserve on the
  media and temporary filesystems.
- `GET /admin/operations/integrity` reports database records with missing files
  and files with no database record.

Container Manager uses `/readyz` for the image health check. JSON logs rotate
according to `LOG_MAX_SIZE` and `LOG_MAX_FILES`. Startup also performs a
non-blocking integrity scan and writes only issue counts to the container log.

Uploads are rejected before chunk or final-file writes would consume
`ENV_MIN_DISK_FREE_BYTES`. Expired authentication records and incomplete
uploads older than `ENV_UPLOAD_RETENTION` are removed every
`ENV_CLEANUP_INTERVAL`.

## Backup

The backup script briefly stops the application container so the PostgreSQL
dump, completed media, and resumable chunks describe one consistent point in
time. PostgreSQL continues running.

```sh
COMPOSE_EXTRA="$PWD/build/compose.synology.yaml" \
  ./scripts/backup.sh /volume1/docker/next-gallery-server/backups
```

Each timestamped backup contains `database.dump`, `media.tar.gz`,
`uploads.tar.gz`, the protected environment snapshot, and `SHA256SUMS`. Copy
backups to another device; a backup on the same NAS is not disaster recovery.

## Restore drill

Test restore on a non-production project first. The command verifies checksums,
stops the application container, replaces database objects and both storage
trees, and then starts the application again:

```sh
FORCE=YES COMPOSE_EXTRA="$PWD/build/compose.synology.yaml" \
  ./scripts/restore.sh /volume1/docker/next-gallery-server/backups/20261001T120000Z
```

After restore, require `GET /readyz` to return `200`, authenticate, and run
`GET /admin/operations/integrity`. Open several photos and videos before
declaring the restore successful.

## Upgrades

Create and copy a verified backup off the NAS before upgrading. Use a specific
release tag rather than `latest`, pull the image, recreate the project, and
check readiness and integrity. Keep the previous image tag until the restore
drill and application checks pass.
