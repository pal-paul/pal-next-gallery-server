# Synology deployment and operations

## Installation

### Prerequisites

- Synology DSM 7 with Container Manager installed.
- An `amd64` or `arm64` NAS.
- A stable NAS IP address or DNS name.
- Permission to create shared folders and Container Manager projects.
- A TOTP authenticator application for account sign-in.

### 1. Prepare directories

Create these directories from DSM File Station or SSH and restrict them to the
account used by Container Manager:

```text
/volume1/docker/next-gallery-server/postgres
/volume1/docker/next-gallery-server/backups
/volume1/media/gallery
/volume1/media/import
/volume1/media/tmp
```

Synology may validate bind-mount sources before Compose processes
`create_host_path`, so create every source directory explicitly. If the NAS
uses a volume other than `volume1`, update every source path in the Compose
file.

Moment descriptions and titles use the hosted Gemini API, so the NAS does not
need local model storage or inference memory. Outbound HTTPS access to
`generativelanguage.googleapis.com` is required when enrichment is enabled.

### 2. Configure the project

Edit `build/compose.synology.yaml` and replace every `<change-me>` value. The
PostgreSQL username and password in `ENV_DATABASE_URL` must match the values on
the `postgres` service. Use a bootstrap password of at least 12 characters for
`ENV_ADMIN_PASSWORD`. Set `ENV_ISSUER` to the name that should appear in the
authenticator application.

Replace `ENV_MOMENTS_GEMINI_API_KEY` with a paid-tier Gemini API key. Paid-tier
requests are not used to improve Google's products; selected photos are still
sent to Google for processing. Leave the value empty to disable automatic
Moment enrichment.

Set `ENV_CORS_ALLOWED_ORIGINS` to the exact browser origin, including scheme and
port but excluding the path and trailing slash. For example:

```yaml
ENV_CORS_ALLOWED_ORIGINS: http://192.168.0.105:8013
```

Use a comma-separated list when more than one origin is required. Also set the
external HTTPS origin and trusted proxy if DSM reverse proxy is used. Do not
include `null` under normal operation.

### 3. Deploy with Container Manager

In DSM, open **Container Manager > Project > Create**, choose the Compose file,
and use `next-gallery-server` as the project name. Review the generated project
and start it. Wait for `postgres` and `server` to report healthy.

The Synology Compose file contains all settings and does not require a `.env`
file. As an alternative, deploy it from SSH while in the repository directory:

```sh
docker compose -f build/compose.synology.yaml pull
docker compose -f build/compose.synology.yaml up -d
```

The `server` service uses the release-managed `latest` image. Publish a new
repository release containing the Moments implementation before deploying this
configuration to a remote NAS; the image publishing workflow updates that tag.

Verify that the server has outbound connectivity to the Gemini API:

```sh
docker compose -f build/compose.synology.yaml exec server \
  wget -qO- https://generativelanguage.googleapis.com/
```

### 4. Complete initial setup

Open the setup page in a normal external browser:

```text
http://NAS_IP:8013/setup
```

Authenticate with `ENV_ADMIN_USERNAME` and `ENV_ADMIN_PASSWORD`, then create the
permanent administrator account. The bootstrap credentials are only used by
this one-time form. After setup succeeds, `/setup` returns `404` by design.

Open `http://NAS_IP:8013/`, sign in with the permanent administrator account,
and enroll it in TOTP when prompted.

### 5. Verify the deployment

Check readiness from another machine on the same network:

```sh
curl -f http://NAS_IP:8013/readyz
```

A successful request returns HTTP `200`. Keep TCP port `8013` limited to the
trusted network, or publish the service through the DSM HTTPS reverse proxy.

## Media import

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
includes FFmpeg, OpenCV, and `/app/moments-cv-worker`, and runs as a non-root
user.

## Moments

Moment generation considers unassigned, processed photos captured within the
previous seven days. A group must contain at least three photos. Gemini receives
images sampled across the group and the Moment is created only when at least
65% of the supplied photos support one shared description. Each photo can
belong to only one Moment.

Change `ENV_MOMENTS_INTERVAL` to control background generation; `168h` runs it
weekly. The embedding endpoint is disabled by default; set
`ENV_MOMENTS_EMBEDDING_URL` only when a compatible private SigLIP/CLIP service
is available.

## Reverse proxy

Use DSM **Login Portal > Advanced > Reverse Proxy** to terminate HTTPS and
forward to the media server port. Set `ENV_CORS_ALLOWED_ORIGINS` to the external
HTTPS origin. Set `ENV_TRUSTED_PROXIES` to the source address or CIDR from which
DSM reaches the container. This value controls whether forwarded client IP and
HTTPS headers are trusted; leave it empty when no reverse proxy is used.

After changing the Compose environment, recreate the application container
(Compose service `server`). Confirm the session cookie is marked `Secure` through the HTTPS
endpoint.

## Cloudflare Tunnel

The Synology Compose project includes a `cloudflared` service that publishes the
application without forwarding a router port. It uses a dedicated Docker
network and fixed proxy address so the application trusts forwarded HTTPS
headers only from `cloudflared`.

### 1. Create the tunnel

In the Cloudflare dashboard, open **Zero Trust > Networks > Tunnels**, create a
Cloudflared tunnel, and choose Docker as the connector type. Copy only the
tunnel token from the generated command. Do not commit or share this token.

Replace the `TUNNEL_TOKEN` `<change-me>` value under the `cloudflared` service in
`build/compose.synology.yaml`. The token grants permission to run the tunnel, so
store the deployed Compose project as a secret-bearing configuration.

### 2. Configure the public hostname

In the tunnel's **Public Hostnames** settings, add:

```text
Hostname: gallery.palpaul.com
Type:     HTTP
URL:      server:8081
```

The service URL uses the Compose service name and internal port, not the NAS IP
or published port `8013`. Cloudflare creates the required DNS record when the
public hostname is saved.

The server configuration must include:

```yaml
ENV_CORS_ALLOWED_ORIGINS: "https://gallery.palpaul.com,http://192.168.0.105:8013"
ENV_TRUSTED_PROXIES: 172.30.0.2/32
```

The first origin permits the public HTTPS site and the second retains direct
LAN access. The trusted address must match the `cloudflared` `ipv4_address` in
the Compose file. Do not trust the entire Docker subnet unless there is a
specific operational need.

### 3. Deploy and verify

Upload or paste the revised Compose project into Container Manager and recreate
the services. Editing a copy on another computer does not update the NAS. From
NAS SSH, the equivalent commands are:

```sh
docker compose -f build/compose.synology.yaml pull
docker compose -f build/compose.synology.yaml up -d --force-recreate server cloudflared
docker compose -f build/compose.synology.yaml logs --tail=100 cloudflared
```

Wait for the logs to report registered tunnel connections, then open:

```text
https://gallery.palpaul.com
```

Check `https://gallery.palpaul.com/readyz` and confirm that authentication sets
the session cookie with the `Secure` attribute. Keep port `8013` restricted to
the local network; Cloudflare Tunnel does not require it to be internet-facing.

For an internet-accessible private gallery, add a Cloudflare Access application
for `gallery.palpaul.com` and restrict it to approved identities. Test uploads,
API calls, and sign-in after enabling Access so its policy covers the intended
clients.

### Cloudflare Tunnel troubleshooting

- **Tunnel is down:** Check `cloudflared` logs and confirm the active
  `TUNNEL_TOKEN` is complete and belongs to the correct tunnel. The NAS must be
  able to make outbound Cloudflare connections; no inbound router port is
  required.
- **Cloudflare 502:** Confirm the public hostname service is exactly
  `http://server:8081`, the `server` container is healthy, and both services are
  attached to the `tunnel` network.
- **Setup or API returns 403:** Confirm the browser sends
  `Origin: https://gallery.palpaul.com` and that this exact value is present in
  `ENV_CORS_ALLOWED_ORIGINS`. Recreate `server` after changing its environment.
- **Login does not persist:** Confirm `ENV_TRUSTED_PROXIES` matches the fixed
  `cloudflared` address and that Cloudflare sends `X-Forwarded-Proto: https`.
  The resulting session cookie must be marked `Secure`.
- **Uploads fail through Cloudflare:** Check the Cloudflare plan's request-size
  limits and Access policies. Direct LAN access can help distinguish a
  Cloudflare limit from an application storage or permission problem.

## Troubleshooting

### Bind mount source does not exist

Container Manager can reject a missing source even when the Compose mount uses
`create_host_path: true`. Create the exact path shown in the error using File
Station or SSH, then redeploy the project:

```sh
mkdir -p /volume1/docker/next-gallery-server/postgres
mkdir -p /volume1/media/gallery /volume1/media/import /volume1/media/tmp
```

Confirm the paths are on the correct Synology volume. If PostgreSQL then reports
permission errors, grant the Container Manager service access to the directory
or set ownership appropriate for the PostgreSQL container.

### Setup submission returns 403

Inspect the failed `/setup` request in the browser developer tools. Its
`Origin` header must exactly match an entry in `ENV_CORS_ALLOWED_ORIGINS`.
Origins include the scheme and port; paths are not included.

An embedded or sandboxed browser may send `Origin: null`. Open `/setup` directly
in Safari, Firefox, or Chrome instead. If a temporary `null` entry is required
for diagnosis, remove it immediately after setup and recreate the `server`
container.

Changing the Compose file on another computer does not update the NAS project,
and restarting an existing container does not apply changed environment
variables. Upload or paste the revised project configuration and recreate the
`server` container. From NAS SSH, verify the active value with:

```sh
docker inspect next-gallery-server-server-1 \
  --format '{{range .Config.Env}}{{println .}}{{end}}' | \
  grep ENV_CORS_ALLOWED_ORIGINS
```

The generated container name may differ; find it in Container Manager or with
`docker ps`.

### Setup returns 401 or 404

- `401 Unauthorized` means the submitted bootstrap username or password does
  not match the active `ENV_ADMIN_USERNAME` and `ENV_ADMIN_PASSWORD` values.
- `404 Not Found` means initial setup is already complete. Sign in at `/`
  using the permanent administrator account.

### Server is unhealthy or unavailable

Check the application and database logs in Container Manager, or use:

```sh
docker compose -f build/compose.synology.yaml ps
docker compose -f build/compose.synology.yaml logs --tail=100 postgres server
curl -i http://127.0.0.1:8013/readyz
```

For PostgreSQL authentication failures, ensure `POSTGRES_USER`,
`POSTGRES_PASSWORD`, and the credentials embedded in `ENV_DATABASE_URL` match.
For connection timeouts, confirm the `8013:8081` port mapping and allow TCP port
`8013` through the DSM firewall. A failing readiness check can also indicate
that available storage is below `ENV_MIN_DISK_FREE_BYTES`.

### Media import or upload permission errors

Confirm that `/volume1/media/gallery`, `/volume1/media/import`, and
`/volume1/media/tmp` exist and are writable by the application container. Also
confirm that each Compose source maps to the expected `/data/...` target. Do not
delete or recreate these directories while the project is running.

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
