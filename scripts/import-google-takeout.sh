#!/usr/bin/env bash

set -euo pipefail

server_url="${PAL_SERVER_URL:-http://127.0.0.1:8081}"
username="${PAL_USERNAME:-}"
moments_enabled="true"
state_file=""
allow_insecure_http="false"
input_path=""

usage() {
	cat <<'EOF'
Usage: import-google-takeout.sh [options] <takeout.zip|directory>

Imports one Google Takeout ZIP or every ZIP directly inside a directory.
Completed archives are recorded by SHA-256 and skipped when the command is
run again. An interrupted archive restarts from the beginning because the
server does not currently support resuming within a ZIP.

Options:
  --server URL             Gallery server (default: PAL_SERVER_URL or http://127.0.0.1:8081)
  --username USERNAME      Login username (default: PAL_USERNAME or prompt)
  --moments-enabled BOOL   Enable automatic Moments for imported media (default: true)
  --state-file PATH        Resume checkpoint file (default: beside the input)
  --allow-insecure-http    Allow credentials over HTTP to a non-local server
  -h, --help               Show this help

Requires: bash, curl, jq, shasum
EOF
}

die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

json_request() {
	local method=$1
	local url=$2
	local payload=$3
	local body_file=$4
	curl --silent --show-error \
		--request "$method" \
		--header 'Content-Type: application/json' \
		--cookie "$cookie_file" \
		--cookie-jar "$cookie_file" \
		--data-binary @- \
		--output "$body_file" \
		--write-out '%{http_code}' \
		"$url" <<< "$payload"
}

is_completed() {
	local checksum=$1
	local tab
	tab=$(printf '\t')
	grep -F -q "${checksum}${tab}" "$state_file"
}

record_completed() {
	local checksum=$1
	local archive=$2
	printf '%s\t%s\t%s\n' "$checksum" "$(basename "$archive")" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" >> "$state_file"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--server)
			[[ $# -ge 2 ]] || die '--server requires a value'
			server_url=$2
			shift 2
			;;
		--username)
			[[ $# -ge 2 ]] || die '--username requires a value'
			username=$2
			shift 2
			;;
		--moments-enabled)
			[[ $# -ge 2 ]] || die '--moments-enabled requires a value'
			moments_enabled=$2
			shift 2
			;;
		--state-file)
			[[ $# -ge 2 ]] || die '--state-file requires a value'
			state_file=$2
			shift 2
			;;
		--allow-insecure-http)
			allow_insecure_http="true"
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		--*)
			die "unknown option: $1"
			;;
		*)
			[[ -z "$input_path" ]] || die 'only one ZIP file or directory may be provided'
			input_path=$1
			shift
			;;
	esac
done

[[ -n "$input_path" ]] || { usage >&2; exit 2; }
[[ -f "$input_path" || -d "$input_path" ]] || die "input does not exist: $input_path"
[[ "$moments_enabled" == "true" || "$moments_enabled" == "false" ]] || die '--moments-enabled must be true or false'

require_command curl
require_command jq
require_command shasum

server_url=${server_url%/}
case "$server_url" in
	https://*) ;;
	http://127.0.0.1:*|http://127.0.0.1|http://localhost:*|http://localhost) ;;
	http://*) [[ "$allow_insecure_http" == "true" ]] || die 'refusing to send credentials over non-local HTTP; use HTTPS or --allow-insecure-http' ;;
	*) die '--server must begin with http:// or https://' ;;
esac

if [[ -z "$state_file" ]]; then
	if [[ -d "$input_path" ]]; then
		state_file="$input_path/.pal-takeout-import.state"
	else
		state_file="${input_path}.pal-import.state"
	fi
fi
mkdir -p "$(dirname "$state_file")"
touch "$state_file"
chmod 600 "$state_file"

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/pal-takeout-import.XXXXXX")
cookie_file="$work_dir/cookies.txt"
response_file="$work_dir/response.json"
archive_list="$work_dir/archives.txt"
trap 'rm -rf "$work_dir"' EXIT INT TERM
touch "$cookie_file"
chmod 600 "$cookie_file"

if [[ -f "$input_path" ]]; then
	case "${input_path##*.}" in
		zip|ZIP) printf '%s\n' "$input_path" > "$archive_list" ;;
		*) die 'input file must have a .zip extension' ;;
	esac
else
	find "$input_path" -maxdepth 1 -type f \( -name '*.zip' -o -name '*.ZIP' \) -print | LC_ALL=C sort > "$archive_list"
fi
[[ -s "$archive_list" ]] || die 'no ZIP archives found'

if [[ -z "$username" ]]; then
	read -r -p 'Username: ' username
fi
[[ -n "$username" ]] || die 'username is required'
read -r -s -p 'Password: ' password
printf '\n'

login_payload=$(jq -cn --arg username "$username" --arg password "$password" '{username: $username, password: $password}')
unset password
login_status=$(json_request POST "$server_url/auth/login" "$login_payload" "$response_file") || die 'could not connect to the gallery server'
unset login_payload
[[ "$login_status" == "200" ]] || die "login failed (HTTP $login_status): $(jq -r '.error // "unknown error"' "$response_file")"

if [[ "$(jq -r '.authenticated // false' "$response_file")" != "true" ]]; then
	challenge_token=$(jq -r '.challengeToken // empty' "$response_file")
	[[ -n "$challenge_token" ]] || die 'login response did not contain an authentication challenge'
	if [[ -n "$(jq -r '.provisioningUri // empty' "$response_file")" ]]; then
		die 'TOTP enrollment is required; complete the first sign-in in the web app, then run this utility again'
	fi
	read -r -p '6-digit authentication code: ' totp_code
	[[ "$totp_code" =~ ^[0-9]{6}$ ]] || die 'authentication code must contain 6 digits'
	verify_payload=$(jq -cn --arg challengeToken "$challenge_token" --arg code "$totp_code" '{challengeToken: $challengeToken, code: $code}')
	unset totp_code challenge_token
	verify_status=$(json_request POST "$server_url/auth/verify" "$verify_payload" "$response_file") || die 'authentication verification request failed'
	unset verify_payload
	[[ "$verify_status" == "200" ]] || die "authentication failed (HTTP $verify_status): $(jq -r '.error // "unknown error"' "$response_file")"
fi

total=$(wc -l < "$archive_list" | tr -d ' ')
current=0
imported_archives=0
skipped_archives=0

while IFS= read -r archive; do
	[[ -n "$archive" ]] || continue
	current=$((current + 1))
	printf '[%d/%d] Checking %s\n' "$current" "$total" "$(basename "$archive")"
	checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
	if is_completed "$checksum"; then
		printf '      Already imported; skipping.\n'
		skipped_archives=$((skipped_archives + 1))
		continue
	fi

	printf '      Uploading and importing...\n'
	set +e
	import_status=$(curl --show-error --progress-bar \
		--request POST \
		--cookie "$cookie_file" \
		--form "archive=@${archive};type=application/zip" \
		--form "momentsEnabled=${moments_enabled}" \
		--output "$response_file" \
		--write-out '%{http_code}' \
		"$server_url/media/import/google-takeout")
	curl_status=$?
	set -e
	if [[ $curl_status -ne 0 ]]; then
		die "connection failed while importing $(basename "$archive"); rerun the command to resume from this archive"
	fi
	if [[ "$import_status" != "201" ]]; then
		die "import failed for $(basename "$archive") (HTTP $import_status): $(jq -r '.error // "unknown error"' "$response_file")"
	fi

	record_completed "$checksum" "$archive"
	imported_archives=$((imported_archives + 1))
	printf '      Imported: %s, duplicates: %s, albums: %s, skipped files: %s\n' \
		"$(jq -r '.imported' "$response_file")" \
		"$(jq -r '.duplicates' "$response_file")" \
		"$(jq -r '.albums' "$response_file")" \
		"$(jq -r '.skipped' "$response_file")"
done < "$archive_list"

printf 'Done. Imported %d archive(s); skipped %d completed archive(s).\n' "$imported_archives" "$skipped_archives"
printf 'Resume state: %s\n' "$state_file"
