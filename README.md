<div align="center">

# Uploader

**A fast, self-hosted file and image host for ShareX and the browser. One Go binary, one Docker image.**

[![CI](https://github.com/ariazonaa/uploader/actions/workflows/ci.yml/badge.svg)](https://github.com/ariazonaa/uploader/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/ariazonaa/uploader?filename=backend%2Fgo.mod)](backend/go.mod)
[![Release](https://img.shields.io/github/v/release/ariazonaa/uploader)](https://github.com/ariazonaa/uploader/releases/latest)
[![Image](https://img.shields.io/badge/ghcr.io-ariazonaa%2Fuploader-2496ED?logo=docker&logoColor=white)](https://github.com/ariazonaa/uploader/pkgs/container/uploader)

English | [Deutsch](README.de.md)

</div>

<p align="center">
  <img src="docs/screenshots/dashboard.webp" alt="Dashboard with uploaded photos, stats and filters" width="900">
</p>

Uploader is a Zipline-style upload server. Point ShareX (or curl, Flameshot, or any
HTTP client) at it, or drag files into the web dashboard, and get back a short link
that unfurls nicely in Discord, Slack, Mastodon and friends. The Go backend serves the
JSON API, the public share pages and the embedded React dashboard from a single binary,
with PostgreSQL as its only dependency.

<p align="center">
  <a href="#quick-start-docker-compose"><b>Quick start</b></a> ·
  <a href="#features"><b>Features</b></a> ·
  <a href="#screenshots"><b>Screenshots</b></a> ·
  <a href="#sharex-and-the-api"><b>ShareX & API</b></a> ·
  <a href="#configuration"><b>Configuration</b></a> ·
  <a href="#development"><b>Development</b></a>
</p>

## Highlights

- 🚀 **One container, one database.** A single Go binary serves the API, the share pages and the dashboard. No Redis, no queue server.
- 📤 **Upload any way you like.** Drag and drop, paste, folders, ShareX, curl, resumable tus uploads for big files.
- 🔗 **Links that embed.** OpenGraph, Twitter cards, oEmbed and real file extensions, so links unfurl in Discord, Slack and Mastodon.
- 🛡️ **Private by default.** EXIF/GPS stripping, optional at-rest encryption, 2FA, a strict CSP and a container that drops root.
- 👥 **Made for small groups.** Invite-only accounts, an admin role, API tokens, an audit log and a trash with restore.
- 🎬 **Media done right.** Thumbnails, video posters and adaptive HLS streaming with MP4 fallback.

## Features

**Uploading**

- Upload from the dashboard by drag and drop (including whole folders), by pasting
  with Ctrl+V, or through the API.
- ShareX support: download a ready-made `.sxcu` config when you create an API token.
- Resumable uploads via the [tus 1.0](https://tus.io) protocol for large files (the
  dashboard switches to tus automatically from 10 MiB).
- Custom slugs (`?slug=my-link`) or automatic ones: random, word-based (`gfycat`) or
  date-based.
- Content-type sniffing from the file bytes (the client's MIME type is never
  trusted), a configurable MIME blocklist and SHA-256 deduplication.

**Sharing and embeds**

- Share pages at `/s/{slug}` with OpenGraph and Twitter Card tags, multiple
  `og:image` entries, `twitter:player` for media, and an oEmbed endpoint.
- Raw links carry the real file extension (`/raw/{slug}.png`), so chat clients
  embed them directly; strong ETags and conditional requests.
- Image thumbnails, video thumbnails and video metadata (via ffmpeg, bundled in the
  image).
- Adaptive HLS streaming for videos, transcoded in the background, with MP4
  fallback.
- A ShareX-style deletion link per file (`/d/{key}`) with a confirmation page.

**Privacy and safety**

- JPEG metadata stripping (GPS, EXIF, XMP, IPTC), on by default. Colour profiles and
  orientation are kept; a file that cannot be cleaned is re-encoded or rejected,
  never stored unstripped.
- Optional at-rest encryption of stored files.
- Trash with restore. Deleted files are purged after a configurable period (30 days
  by default), and an optional global retention policy expires old files.

**Accounts and administration**

- First-run setup wizard in the browser: no CLI needed to create the first admin.
- Multiple users through single-use invite codes, with an admin role.
- Two-factor authentication (TOTP) with recovery codes.
- Personal API tokens, a session list with remote sign-out, data export (ZIP
  with a JSON manifest) and account deletion.
- Admin settings page for branding, embeds, upload limits and retention, plus view
  statistics per file and an audit log.
- Web Push notifications (optional, VAPID).
- Dashboard in English and German.

**Operations**

- A single Docker image that runs read-only, drops root on its own and needs only
  PostgreSQL.
- Database migrations run automatically on start.
- Tunable connection pool and an optional read replica for read-heavy queries;
  sessions live in the database, so several instances can share one database.
- OpenAPI 3 specification with a built-in, offline Swagger UI.

## Screenshots

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/share.webp" alt="Public share page"><br><sub><b>Share page</b> — what the people you send a link to see, with OpenGraph tags for rich embeds.</sub></td>
    <td width="50%"><img src="docs/screenshots/lightbox.webp" alt="Lightbox preview in the dashboard"><br><sub><b>Lightbox</b> — full-size preview straight from the dashboard.</sub></td>
  </tr>
  <tr>
    <td><img src="docs/screenshots/profile.webp" alt="Profile with statistics, achievements and upload heatmap"><br><sub><b>Profile</b> — upload statistics, achievements and a yearly activity heatmap.</sub></td>
    <td><img src="docs/screenshots/settings-embeds.webp" alt="Admin settings with Discord embed preview"><br><sub><b>Settings</b> — embed templates with a live Discord preview; all runtime settings live in the dashboard.</sub></td>
  </tr>
  <tr>
    <td><img src="docs/screenshots/traffic.webp" alt="Traffic statistics by referrer and client"><br><sub><b>Traffic</b> — where views come from, by referrer and client type.</sub></td>
    <td align="center">
      <img src="docs/screenshots/mobile-dashboard.webp" alt="Dashboard on a phone" width="45%">
      <img src="docs/screenshots/mobile-share.webp" alt="Share page on a phone" width="45%"><br>
      <sub><b>Mobile</b> — the dashboard and share pages work on phones too.</sub>
    </td>
  </tr>
</table>

<sub>Demo photos from <a href="https://unsplash.com">Unsplash</a> via <a href="https://picsum.photos">Lorem Picsum</a>.</sub>

## Quick start (Docker Compose)

You need Docker with the Compose plugin and `openssl`.

```bash
git clone https://github.com/ariazonaa/uploader.git
cd uploader
cp .env.example .env

# Generate two DIFFERENT 32-byte keys and paste them into .env:
openssl rand -hex 32   # -> UPLOADER_SESSION_HASH_KEY
openssl rand -hex 32   # -> UPLOADER_SESSION_BLOCK_KEY
# Also set POSTGRES_PASSWORD in .env.

docker compose up -d
```

This pulls the prebuilt image `ghcr.io/ariazonaa/uploader` together with PostgreSQL 17.
All persistent data is bind-mounted under `./data`: `./data/files` for uploads and
`./data/postgres` for the database.

Then open the app in your browser. As long as no account exists, it shows a setup
wizard that creates the first admin. Afterwards, go to **Settings** and set the
**public URL** so generated links point to the right host.

### Reverse proxy

The bundled `docker-compose.yml` does not publish a port. It expects a reverse proxy
in front that terminates TLS and forwards to the `app` container on port 8080. The
stack uses its own bridge network with fixed addresses, so a proxy on the host can
reach the app at `10.89.0.42:8080` by default. An nginx example is in
[`deploy/nginx/example.conf`](deploy/nginx/example.conf). If the default subnet
clashes with one of your networks, change `UPLOADER_SUBNET`, `UPLOADER_GATEWAY`,
`UPLOADER_APP_IP` and `UPLOADER_POSTGRES_IP` in `.env`.

The proxy should append to `X-Forwarded-For` (`$proxy_add_x_forwarded_for` in nginx)
and should not add security headers of its own: the app sets them, and some routes
deliberately relax them (for example, `/p/{slug}` may be framed for embeds).

### Local testing without a proxy

To try it on your own machine, add the development overlay. It publishes the app on
`http://127.0.0.1:8080` and turns off secure cookies and proxy trust for plain HTTP:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
```

The overlay binds to 127.0.0.1 only. Never use it on a public server.

### Creating users from the CLI

The setup wizard is the usual way, but the first admin can also be created on the
command line:

```bash
docker compose exec app uploader user create --username admin --email you@example.com --password '…'
```

Further users join with invite codes. Set `UPLOADER_REGISTRATION=invite`, then create
codes under **Invites** in the dashboard.

## Configuration

Configuration is split into two kinds of settings.

- **Infrastructure and secrets** come from environment variables (normally your
  `.env`). They are listed below.
- **Everything else is configured in the admin dashboard (Settings), never through
  environment variables.** This covers the app name, public URL, embed templates and
  colour, branding (logo, login page text and background, "powered by" badge), the
  legal notice, the maximum upload size, EXIF stripping, the encryption default, the
  slug strategy, the blocked MIME types, and the retention periods for files, trash
  and the audit log. These values are stored in the database, seeded with defaults on
  first start, and survive restarts and upgrades.

| Variable | Default | Description |
| --- | --- | --- |
| `UPLOADER_SESSION_HASH_KEY` | *required* | 64 hex chars (`openssl rand -hex 32`). Signs the session cookie. |
| `UPLOADER_SESSION_BLOCK_KEY` | *required* | 64 hex chars, different from the hash key. Encrypts the session cookie and the stored 2FA secrets. |
| `POSTGRES_PASSWORD` | *required* | Database password, used by the Compose stack for both containers. |
| `UPLOADER_DATABASE_URL` | set by Compose | PostgreSQL connection string. Only needed when you run the binary yourself. |
| `UPLOADER_DATABASE_REPLICA_URL` | *empty* | Optional read replica for read-heavy queries. Writes always go to the primary. |
| `UPLOADER_DB_MAX_CONNS` / `UPLOADER_DB_MIN_CONNS` | `0` (built-in: 12 / 2) | Connection pool size. |
| `UPLOADER_DB_MAX_CONN_LIFETIME` / `UPLOADER_DB_MAX_CONN_IDLE_TIME` | *empty* (built-in: `1h` / `30m`) | Connection pool lifetimes (Go durations). |
| `UPLOADER_ENCRYPTION_KEY` | *empty* | 64 hex chars, the key for at-rest encryption. If empty, it is derived from the session block key; see [Key rotation](#key-rotation). |
| `UPLOADER_REGISTRATION` | `off` | `off` or `invite` (anyone with a valid invite code can register). |
| `UPLOADER_TRUST_PROXY` | `true` in Compose, `false` in the binary | Honour `X-Forwarded-*` headers. See [Reverse proxy and client IPs](#reverse-proxy-and-client-ips). |
| `UPLOADER_CLIENT_IP_HEADER` | *empty* | Read the client IP only from this header (for example `X-Real-IP` or `CF-Connecting-IP`). |
| `UPLOADER_SECURE_COOKIES` | `true` | Set to `false` only for plain-HTTP testing. |
| `UPLOADER_SESSION_TTL` | `720h` | Session lifetime. |
| `UPLOADER_SLUG_LENGTH` | `6` | Length of random slugs (minimum 4). |
| `UPLOADER_VAPID_PUBLIC_KEY` / `UPLOADER_VAPID_PRIVATE_KEY` | *empty* | Web Push keys; generate with `uploader vapid`. Push stays off while either is empty. |
| `UPLOADER_VAPID_SUBJECT` | `mailto:admin@example.com` | Contact URI sent to push services. |
| `UPLOADER_SEED_PASSWORD` | *empty* | Password used by `uploader user create` when `--password` is omitted. |
| `UPLOADER_HTTP_ADDR` | `:8080` | Listen address (not passed through by Compose; only relevant when you run the binary yourself). |
| `UPLOADER_STORAGE_FILE_ROOT` | `./data/files` (`/data/files` in Compose) | Where uploads are stored. |
| `UPLOADER_IMAGE` / `UPLOADER_TAG` | `ghcr.io/ariazonaa/uploader` / `latest` | Which image Compose pulls. See [Updating](#updating-and-pinning-a-version). |
| `UPLOADER_SUBNET` / `UPLOADER_GATEWAY` | `10.89.0.40/29` / `10.89.0.41` | Compose only: the stack's bridge network. |
| `UPLOADER_APP_IP` / `UPLOADER_POSTGRES_IP` | `10.89.0.42` / `10.89.0.43` | Compose only: fixed container addresses (the proxy targets the app IP). |

Optional variables that are left empty in `.env` keep their built-in default.

### Reverse proxy and client IPs

`UPLOADER_TRUST_PROXY` has no universally safe value; it must match how you deploy.

- **Behind a proxy, set to `false`:** every request appears to come from the proxy,
  so all clients share one rate-limit bucket. Five failed logins from anyone lock the
  login for everyone.
- **Directly reachable, set to `true`:** clients can claim any IP they like.

When it is on, the app reads the **rightmost** `X-Forwarded-For` entry (the hop your
own proxy appended), falling back to `X-Real-IP`. If your proxy sets only one of these
headers, the other one passes through from the client untouched. In that case set
`UPLOADER_CLIENT_IP_HEADER` to the header your proxy actually writes.

### Key rotation

`UPLOADER_SESSION_BLOCK_KEY` encrypts the stored 2FA secrets. If
`UPLOADER_ENCRYPTION_KEY` is empty, the at-rest encryption key is derived from it as
well. Rotating the session key therefore makes encrypted files unreadable and
invalidates all 2FA enrolments. **Set `UPLOADER_ENCRYPTION_KEY` explicitly before you
enable encryption in production.** For 2FA, re-enrolling is the only way back. The
server logs a warning on start while the encryption key is derived.

## ShareX and the API

Create an API token under **Tokens** in the dashboard. The token is shown only once,
and the same dialog offers a ready-to-import ShareX `.sxcu` file.

Uploads are a multipart `POST` to `/api/v1/upload` with the file in the `file` field.
Authenticate with `Authorization: Bearer upl_…` (or `X-Uploader-Token: upl_…`).

```bash
curl -F "file=@picture.png" -H "Authorization: Bearer upl_…" https://i.example.com/api/v1/upload

# Flameshot (Linux): screenshot straight to the server
flameshot gui --raw | curl -F "file=@-;filename=shot.png" \
  -H "Authorization: Bearer upl_…" https://i.example.com/api/v1/upload
```

The JSON response contains `url` (share page), `raw` (direct link), `thumb` (when a
thumbnail exists) and `delete` (public deletion link), among other fields.

Optional query parameters:

| Parameter | Effect |
| --- | --- |
| `slug=my-link` | Custom slug (3–64 letters, digits, `-` or `_`). `409` if it is taken. |
| `slug_strategy=random\|gfycat\|date` | Naming scheme for automatic slugs. |
| `strip_exif=false` | Keep image metadata for this upload. |
| `encrypt=true\|false` | Override the encryption default for this upload. |

Large files can use the tus endpoint at `/api/v1/tus` instead.

The complete API is described as an OpenAPI 3 document at `/api/v1/openapi.yaml`
(sign-in or token required). An interactive Swagger UI is available in the dashboard
under **API** (`/docs`). It is served from the app itself, with no external CDN. You
can generate a typed client from the spec, for example:

```bash
curl -H "Authorization: Bearer upl_…" https://i.example.com/api/v1/openapi.yaml -o openapi.yaml
npx @openapitools/openapi-generator-cli generate -i openapi.yaml -g typescript-fetch -o ./client
```

## Updating and pinning a version

```bash
docker compose pull && docker compose up -d
```

Migrations run automatically on start. By default, Compose pulls
`ghcr.io/ariazonaa/uploader:latest`. For production, pin a specific tag in `.env` so
that an upgrade only happens when you choose it:

```env
UPLOADER_TAG=<tag or commit SHA>
```

`UPLOADER_IMAGE` selects a different image repository, for example a mirror or your
own build. To build the image locally from source instead of pulling it, use the
build overlay:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

### CLI

The binary (and therefore the container) has a few subcommands:

```text
uploader serve                                  start the server (runs migrations first)
uploader migrate [up|down|status|reset]         manage database migrations
uploader user create --username U --email E --password P   create an admin
uploader user disable-2fa --username U          turn off 2FA for a locked-out user
uploader seed                                   demo admin and sample files (development)
uploader vapid                                  generate a Web Push key pair
uploader healthcheck                            check /api/v1/ready (used by the image)
uploader version
```

## Development

Prerequisites: **Go 1.27**, **Node.js 22+** and **PostgreSQL 13+** (the Compose file's
`postgres` service works fine).

Use two terminals:

```bash
# Terminal 1: database (published on 127.0.0.1:5432 by the dev overlay) and backend
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d postgres
cd backend
UPLOADER_DATABASE_URL='postgres://uploader:<POSTGRES_PASSWORD>@localhost:5432/uploader?sslmode=disable' UPLOADER_SESSION_HASH_KEY=<64 hex chars> UPLOADER_SESSION_BLOCK_KEY=<64 different hex chars> UPLOADER_SECURE_COOKIES=false go run ./cmd/uploader serve

# Terminal 2: Vite dev server, proxying the API and public routes to :8080
cd frontend
npm ci
npm run dev
```

The database password is the `POSTGRES_PASSWORD` from your `.env`. Use fixed session
keys (for example the ones from `.env`); fresh keys on every start would sign you out
each time. `go run ./cmd/uploader seed` creates a demo admin with sample files.

### Checks

Backend (in `backend/`):

```bash
gofmt -l .                                   # must print nothing
go vet ./...
go test ./...
go build ./...
GOOS=linux GOARCH=amd64 go build ./...       # the container target; some code is Linux-only
GOOS=linux GOARCH=amd64 golangci-lint run    # lint for the container target as well
```

Integration tests run against a real PostgreSQL (13+) and sit behind the
`integration` build tag. Point `UPLOADER_TEST_DATABASE_URL` at a **disposable** server
whose role may `CREATE DATABASE`. Each test package migrates a template database and
gives every test a fresh clone. Without the variable, the tests are skipped.

```bash
UPLOADER_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
go test -tags integration -count=1 ./...
```

Frontend (in `frontend/`):

```bash
npm run typecheck
npm run build                                 # the production build that gets embedded
npx prettier --check "src/**/*.{ts,tsx,css}"
```

### Tech stack

- **Backend:** Go, `chi`, PostgreSQL via `pgx` with hand-written SQL, embedded `goose`
  migrations, `gorilla/securecookie`, `disintegration/imaging`.
- **Frontend:** React 19, Vite, TypeScript, Tailwind CSS v4, TanStack Query,
  `lucide-react`. It is embedded into the Go binary with `go:embed` for production.
- **Media:** ffmpeg and ffprobe as static binaries from `mwader/static-ffmpeg`.
- **Storage:** the local filesystem, sharded by slug.

## Security

A short overview. Please report vulnerabilities as described in
[SECURITY.md](SECURITY.md) rather than in a public issue.

- **Container:** the image starts as root only to fix ownership of the data
  directory, then drops to uid 10001 inside the binary (no `su-exec` or entrypoint
  script). The Compose stack runs it with a read-only root filesystem,
  `no-new-privileges`, all capabilities dropped except the five the privilege drop
  needs, and memory and CPU limits.
- **Headers:** a strict Content-Security-Policy (no inline or external scripts),
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` and HSTS over HTTPS.
  Potentially active content (HTML, SVG, XML, JavaScript) is always served as a
  download, so uploads can't be used for stored XSS.
- **Uploads:** MIME types are sniffed from the content, and JPEG metadata is
  stripped by default. Image decoding is capped (50 megapixels, 16384 px per edge, a
  shared memory budget) against decompression bombs, and ffmpeg runs with time and
  output limits.
- **Authentication:** sessions are encrypted and signed cookies (`HttpOnly`,
  `Secure`, `SameSite=Lax`) backed by a server-side session row. API tokens are
  stored only as hashes. TOTP codes are single-use. Login, 2FA and uploads are rate
  limited per IP (IPv6 by /64) and per account.
- **Web Push:** only endpoints of known push services are accepted, so push
  subscriptions can't be used to reach your internal network.

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) before
opening a pull request. Some features have been considered and deliberately left out
(for example S3 storage, folders and tags, or password-protected files), so for
anything larger, open an issue to discuss it first.

## License

Uploader is released under the [MIT License](LICENSE).
