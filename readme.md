# Next Gallery Server

Go API for multi-user, resumable media uploads. Administrators manage accounts;
regular users upload into their own folders and can share their current and
future media library with other regular users. Managed accounts use TOTP.

The full backend entrypoint is [`cmd/server`](cmd/server). Container images run
it as `/app/pal-media-server`; the Compose service is named `server`. The
upload-specific implementation continues to live under `app/uploader`.

## Requirements

- Docker with Docker Compose
- An amd64 or arm64 NAS/host
- A TOTP authenticator application
- `curl`, `jq`, `file`, `shasum`, and `split` for the API examples

## Local setup

Create the environment file and replace the example passwords:

```bash
cp .env.example .env
```

`ENV_ADMIN_USERNAME` and `ENV_ADMIN_PASSWORD` are bootstrap credentials used
only by the one-time `/setup` form; they are never stored as an account. The
bootstrap password must contain at least 12 characters.

Start the API and PostgreSQL:

```bash
docker compose --env-file .env -f build/compose.yaml up --build
```

The example configuration exposes the gallery and API at
`http://127.0.0.1:8081`. Change `ENV_PORT` in `.env` if that port is occupied.

Open `http://localhost:8081/setup`, enter the bootstrap credentials, and choose
the permanent administrator username and password. The page returns `404`
after the first administrator is created. On first login, that administrator
must scan the returned TOTP provisioning URI and verify a current code.

After setup, open `http://127.0.0.1:8081/` to sign in to the gallery. Set
`ENV_ADMIN_CONFIG=NO` and restart to disable only the administrator account
configuration page. The gallery, setup lifecycle, and authenticated JSON APIs
remain available. Set it back to `YES` and restart to create or manage users in
the browser.

Regular users can upload one or more photos or videos from the upload button in
the gallery header. The browser sends large files through the resumable chunked
upload API and refreshes the library after completion.

For a local source build, run `npm ci && npm run build` in `web` before starting
the Go server. `ENV_WEB_DIR` defaults to `./web/dist`; the container image builds
and installs this directory automatically.

### Frontend demo mode

To test the gallery UI without PostgreSQL, accounts, uploads, or the Go API, run:

```bash
cd web
npm ci
npm run dev:demo
```

Open the URL printed by Vite (normally `http://localhost:5173`). Demo mode skips
sign-in and loads local sample albums and media. The data covers album details,
favorites, trash, map markers, photo/video filters, file metadata, and storage
statistics. Create, edit, reorder, favorite, trash, and restore actions work in
memory and reset when the page reloads. Sample images and map tiles require an
internet connection.

Use `npm run dev` with the Go server running on port `8081` when testing real
authentication, persistence, uploads, or API integration.

Automatic albums are reconciled nightly. Configure `ENV_AUTO_ALBUM_RUN_AT`
(default `02:00`) and `ENV_AUTO_ALBUM_TIMEZONE` (for example,
`Europe/Stockholm`) in `.env` to choose the local run time.

Stop the services while retaining uploaded media and database data:

```bash
docker compose --env-file .env -f build/compose.yaml down
```

Add `--volumes` only when you intentionally want to delete all local data.

## Authentication

`GET /setup`, `POST /setup`, `POST /auth/login`, and `POST /auth/verify` are
public. Setup is available only before the first account exists and while
frontend pages are enabled. All other endpoints require the
`pal_medias_uploader_session` cookie and enforce the account role.

Set values used by the examples:

```bash
BASE_URL=http://localhost:8081
COOKIE_JAR=/tmp/pal-uploader-cookies.txt
USERNAME=admin
PASSWORD='your-admin-password'
```

Begin login as the configured administrator. On first login, the response also
contains `provisioningUri` and `secret`; scan either with the administrator's
authenticator:

```bash
LOGIN_RESPONSE=$(curl -fsS \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg username "$USERNAME" \
    --arg password "$PASSWORD" \
    '{username:$username,password:$password}')" \
  "$BASE_URL/auth/login")
echo "$LOGIN_RESPONSE" | jq
```

Continue with the TOTP verification steps below to save the session cookie.
Then open `http://localhost:8081/admin/config` in a browser, or use
the API to create a managed administrator and at least one regular test user:

```bash
curl -fsS -b "$COOKIE_JAR" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg username alice \
    --arg password replace-with-12-chars \
    '{username:$username,password:$password,
      role:"user",uploadFolder:"alice"}')" \
  "$BASE_URL/admin/users" | jq
```

The creation response contains a one-time `qrCode` data URI and
`provisioningUri`. Scan either with the new user's authenticator. Accounts
created here, including additional administrators, require TOTP.

For any account, begin login and capture its challenge:

```bash
LOGIN_RESPONSE=$(curl -fsS \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg username "$USERNAME" --arg password "$PASSWORD" \
    '{username:$username,password:$password}')" \
  "$BASE_URL/auth/login")
CHALLENGE_TOKEN=$(echo "$LOGIN_RESPONSE" | jq -r '.challengeToken')
```

Verify a current six-digit TOTP code and save the session cookie:

```bash
printf 'TOTP code: '
read -r TOTP_CODE

curl -fsS -c "$COOKIE_JAR" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg challengeToken "$CHALLENGE_TOKEN" \
    --arg code "$TOTP_CODE" \
    '{challengeToken:$challengeToken,code:$code}')" \
  "$BASE_URL/auth/verify" | jq
```

Confirm the session:

```bash
curl -fsS -b "$COOKIE_JAR" "$BASE_URL/auth/session" | jq
```

## Upload media

Upload endpoints accept regular-user sessions only. Completed paths are placed
under that user's configured folder. Active sessions and pending bytes are
limited per user by `ENV_MAX_ACTIVE_UPLOADS_PER_USER` and
`ENV_MAX_PENDING_UPLOAD_BYTES_PER_USER`. The completed-plus-pending total is
limited by `ENV_MAX_STORAGE_BYTES_PER_USER`. `ENV_HTTP_READ_TIMEOUT` bounds
the time allowed to receive each request body.

Choose a real media file and calculate its metadata:

```bash
MEDIA_FILE="$PWD/test/test-video.mp4"
FILE_NAME=$(basename "$MEDIA_FILE")
FILE_SIZE=$(wc -c < "$MEDIA_FILE" | tr -d ' ')
MIME_TYPE=$(file -b --mime-type "$MEDIA_FILE")
SHA256=$(shasum -a 256 "$MEDIA_FILE" | awk '{print $1}')
```

Create an upload session:

```bash
CREATE_RESPONSE=$(curl -fsS -b "$COOKIE_JAR" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg filename "$FILE_NAME" \
    --arg mime_type "$MIME_TYPE" \
    --arg sha256 "$SHA256" \
    --argjson size "$FILE_SIZE" \
    '{filename:$filename,mime_type:$mime_type,size:$size,sha256:$sha256}')" \
  "$BASE_URL/media/upload")

echo "$CREATE_RESPONSE" | jq
UPLOAD_ID=$(echo "$CREATE_RESPONSE" | jq -r '.id')
CHUNK_SIZE=$(echo "$CREATE_RESPONSE" | jq -r '.chunk_size')
```

Split the file and upload every part. Part numbers are zero-based, and the
chunk endpoint requires `PUT` with raw bytes as its body.

```bash
CHUNK_DIR=$(mktemp -d)
split -b "$CHUNK_SIZE" "$MEDIA_FILE" "$CHUNK_DIR/part-"

PART_NUMBER=0
for CHUNK in "$CHUNK_DIR"/part-*; do
  echo "Uploading part $PART_NUMBER"
  curl -fsS -X PUT -b "$COOKIE_JAR" \
    -H 'Content-Type: application/octet-stream' \
    --data-binary "@$CHUNK" \
    "$BASE_URL/media/upload/$UPLOAD_ID/parts/$PART_NUMBER" | jq
  PART_NUMBER=$((PART_NUMBER + 1))
done
```

Inspect progress before completion:

```bash
curl -fsS -b "$COOKIE_JAR" \
  "$BASE_URL/media/upload/$UPLOAD_ID" | jq
```

Assemble, verify, and persist the media:

```bash
curl -fsS -X POST -b "$COOKIE_JAR" \
  "$BASE_URL/media/upload/$UPLOAD_ID/complete" | jq

rm -rf "$CHUNK_DIR"
```

The completion response contains the relative `media_path`, final `size`, and
server-computed `sha256`. Metadata is stored in `media_uploads`, and the file is
stored in the Compose `media_data` volume.

Cancel an incomplete upload and remove its temporary parts:

```bash
curl -fsS -X DELETE -b "$COOKIE_JAR" \
  "$BASE_URL/media/upload/$UPLOAD_ID" | jq
```

## Share media

List files owned by or shared with the current regular user:

```bash
curl -fsS -b "$COOKIE_JAR" "$BASE_URL/media" | jq
```

An owner can use any active completed upload to share their entire library with
another regular user. Existing media becomes available immediately, future
uploads are included automatically, and nightly automatic albums use the
recipient's full accessible library:

```bash
curl -fsS -X POST -b "$COOKIE_JAR" -H 'Content-Type: application/json' \
  -d '{"username":"bob"}' "$BASE_URL/media/files/$UPLOAD_ID/shares"

curl -fsS -b "$COOKIE_JAR" -OJ "$BASE_URL/media/files/$UPLOAD_ID/download"
```

## API endpoints

<!-- markdownlint-disable MD013 -->

| Method   | Path                              | Authentication | Purpose                                  |
| -------- | --------------------------------- | -------------- | ---------------------------------------- |
| `GET`    | `/setup`                          | Public         | Open one-time administrator setup        |
| `POST`   | `/setup`                          | Public         | Create the first administrator           |
| `POST`   | `/auth/login`                     | Public         | Create a password and TOTP challenge     |
| `POST`   | `/auth/verify`                    | Public         | Verify TOTP and issue a session cookie   |
| `GET`    | `/auth/session`                   | Cookie         | Return the authenticated username        |
| `POST`   | `/auth/logout`                    | Cookie         | Delete the session and expire the cookie |
| `GET`    | `/admin/config`                   | Admin          | Open the user configuration page         |
| `GET`    | `/admin/users`                    | Admin          | List users                               |
| `POST`   | `/admin/users`                    | Admin          | Create a TOTP-enabled account            |
| `PUT`    | `/users/me/folder`                | Regular user   | Change the user's upload folder          |
| `GET`    | `/media`                          | Authenticated  | List owned and shared media              |
| `GET`    | `/media/files/{id}/download`      | Authenticated  | Download accessible media                |
| `POST`   | `/media/files/{id}/shares`        | Owner          | Share the owner's media library          |
| `DELETE` | `/media/files/{id}/shares`        | Owner          | Remove the `?username=bob` library share |
| `PATCH`  | `/media/files/{id}/favorite`      | Authenticated  | Set a personal favorite state            |
| `DELETE` | `/media/files/{id}`               | Owner          | Move owned media to trash                |
| `PATCH`  | `/media/files/{id}/restore`       | Owner          | Restore owned media                      |
| `DELETE` | `/media/files/{id}/permanent`     | Owner          | Delete owned media and its thumbnail     |
| `GET`    | `/albums`                         | Authenticated  | List owned albums                        |
| `POST`   | `/albums`                         | Authenticated  | Create an album                          |
| `GET`    | `/albums/{albumId}`               | Authenticated  | Get an album and accessible media        |
| `PATCH`  | `/albums/{albumId}`               | Owner          | Update an album                          |
| `DELETE` | `/albums/{albumId}`               | Owner          | Delete an album                          |
| `GET`    | `/storage`                        | Authenticated  | Get accessible media totals              |
| `POST`   | `/media/upload`                   | Cookie         | Create a resumable upload                |
| `GET`    | `/media/upload/{id}`              | Cookie         | List uploaded parts                      |
| `PUT`    | `/media/upload/{id}/parts/{part}` | Cookie         | Upload or replace one part               |
| `POST`   | `/media/upload/{id}/complete`     | Cookie         | Assemble and persist the media           |
| `DELETE` | `/media/upload/{id}`              | Cookie         | Cancel an incomplete upload              |

<!-- markdownlint-enable MD013 -->

The complete OpenAPI 3.1 contract is in
[spec/openapi.yaml](spec/openapi.yaml). Domain boundaries, multi-user ownership,
and the complete gallery route set are described in
[docs/backend-api.md](docs/backend-api.md).

Synology Container Manager deployment, reverse-proxy, health monitoring,
backup, restore, and integrity procedures are documented in
[docs/synology.md](docs/synology.md).

## Development checks

Run Go checks:

```bash
gofmt -w app cmd
go vet ./...
go test ./...
```

Validate the API specification:

```bash
npx --yes @redocly/cli lint --config spec/redocly.yaml spec/openapi.yaml
```
