# Xin - Pixiv + Telegram Gallery (Go)

Xin is a Go-based illustration gallery system.

It ingests images from:
- Pixiv bookmarks
- Telegram bot messages (photo/document)
- Telegram links (Pixiv artwork links and yande.re post links)

It stores:
- Image files in a Telegram channel (via file_id)
- Metadata in Cloudflare D1

It provides:
- Gallery page
- Favorites page
- Admin upload/management page

## Architecture

Data flow:
1. Ingest image
2. Upload preview + origin to Telegram channel
3. Save metadata to D1
4. Frontend reads metadata from API and fetches image via `/image/{file_id}`

Core components:
- `cmd/server/main.go`: app entrypoint, HTTP server, Telegram bot startup
- `internal/config/config.go`: env config loading
- `internal/app/`: ingest + crawler business logic
- `internal/database/d1.go`: D1 access and schema ensure
- `internal/pixiv/pixiv.go`: Pixiv API calls and image download
- `internal/telegram/telegram.go`: Telegram upload/download utilities
- `internal/web/server.go`: page and API routes
- `web/`: frontend assets

## Features

### 1) Pixiv bookmark crawler
- Scheduled crawler for user bookmarks
- Uploads preview + origin to Telegram channel
- Writes records to D1
- Supports tag filter (`PIXIV_TAG`)
- Supports public/private bookmark source (`PIXIV_REST`)
- Supports crawl order (`PIXIV_CRAWL_ORDER`)

### 2) Telegram direct image ingest
- User sends photo/document to bot
- Bot forwards/normalizes into channel storage
- Metadata is saved to D1

### 3) Telegram link ingest
Supported links:
- Pixiv artwork: `https://www.pixiv.net/artworks/<id>`
- yande.re post: `https://yande.re/post/show/<id>`

Behavior:
- Pixiv link: use Pixiv title/artist/artist_id
- yande.re link: fixed metadata
  - `title = Yandex`
  - `artist_name = Arts`
  - `artist_id = none`
  - `source_url = original yande.re link`
- Max links per message: 3

### 4) Admin management
- Admin upload page with Basic Auth
- Thumbnail management wall
- Favorite toggle
- Hide action (soft delete + blocklist)

### 5) Anti-reingest mechanism
- Hide action does:
  - `images.status = hidden`
  - insert into `ingest_blocklist`
  - remove from `favorites`
- Crawler checks blocklist and skips blocked IDs

### 6) Favorites system
- Separate favorites page
- Admin can mark/unmark favorite
- Public favorites API available

### 7) Random image API (preview-only)
- `/api/random`
- `/api/random?type=h`
- `/api/random?type=v`
- Intentionally removes `origin_id` in response

## Requirements

- Go 1.21+
- Reachable network to Telegram API and Pixiv
- Cloudflare D1 credentials

## Environment Variables

Required:
- `BOT_TOKEN`
- `CHANNEL_ID`
- `CLOUDFLARE_ACCOUNT_ID`
- `CLOUDFLARE_API_TOKEN`
- `D1_DATABASE_ID`
- `ADMIN_PASSWORD`

Pixiv:
- `PIXIV_PHPSESSID`
- `PIXIV_USER_ID`
- `PIXIV_TAG` (optional; empty means no tag filter)
- `PIXIV_REST` (optional; `show` or `hide`, default `show`)
- `PIXIV_CRAWL_ORDER` (optional; `desc` or `asc`, default `desc`)
- `PIXIV_LIMIT` (default `40`)
- `PIXIV_MAX_PAGES` (legacy fallback, default `0` = unlimited)
- `PIXIV_BOOTSTRAP_MAX_PAGES` (default `-1`; inherit `PIXIV_MAX_PAGES`)
- `PIXIV_INCREMENTAL_MAX_PAGES` (default `2`)
- `PIXIV_INTERVAL_MINUTES` (default `120`)

Server:
- `LISTEN_ADDR` (default `:8080`)

## Bootstrap vs Incremental Crawl

State key in D1 table `crawler_state`:
- `key = pixiv_bootstrap_done`
- value `1` means incremental mode
- missing/`0` means bootstrap mode

Page limit selection:
- Bootstrap mode: `PIXIV_BOOTSTRAP_MAX_PAGES` (or fallback to `PIXIV_MAX_PAGES`)
- Incremental mode: `PIXIV_INCREMENTAL_MAX_PAGES`

If initial full crawl is already done, set incremental mode manually:

```sql
CREATE TABLE IF NOT EXISTS crawler_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

INSERT OR REPLACE INTO crawler_state (key, value, updated_at)
VALUES ('pixiv_bootstrap_done', '1', strftime('%s','now'));
```

## Run Locally

```bash
go run ./cmd/server
```

## HTTP Routes

Pages:
- `GET /` -> redirects to `/gallery`
- `GET /gallery`
- `GET /favorites`
- `GET /admin/upload` (Basic Auth)

Public APIs:
- `GET /api/posts?type=all|h|v&offset=0&limit=20`
- `GET /api/favorites?type=all|h|v&offset=0&limit=20`
- `GET /api/random`
- `GET /api/random?type=h`
- `GET /api/random?type=v`
- `GET /image/{file_id}`
  - optional `?dl=1` to force download header

Admin APIs (Basic Auth):
- `GET /admin/api/images?status=active|hidden|all&offset=0&limit=60`
- `POST /admin/api/images/hide`
  - body: `{"id":"...", "reason":"admin_hide"}`
- `POST /admin/api/images/favorite`
  - body: `{"id":"...", "on":true|false}`

## Database

On startup, backend calls `EnsureSchema` and auto-upgrades schema:
- creates missing tables: `favorites`, `ingest_blocklist`, `crawler_state`
- adds `status` column to `images` if missing

Canonical schema file:
- `schema.sql`

Current logical tables:
- `images`
- `favorites`
- `ingest_blocklist`
- `crawler_state`

## Deployment Notes

Typical setup:
- Backend on Zeabur (for example: `pic.mtcacg.top`)
- Frontend static on EdgeOne/Pages (for example: `tyr.mtcacg.top`)

## Umami Analytics (optional)

Integrated frontend events:
- `filter_switch`
- `image_open`
- `source_click`
- `download_click`
- `admin_upload_result`

Set your Umami website id in these files:
- `web/gallery.html` -> `data-umami-website-id`
- `web/favorites.html` -> `data-umami-website-id`
- `web/admin/upload.html` -> `data-umami-website-id`

Current Umami host is preset to `https://umamii.zeabur.app`.

If Zeabur fails pulling GHCR image (401):
- Make package public, or
- Configure GHCR credentials in Zeabur

If Telegram bot reports `409 terminated by other getUpdates request`:
- Only run one bot instance

## Troubleshooting

1. Telegram timeout errors
- Example: `sendPhoto ... context deadline exceeded`
- Usually network instability to Telegram API

2. Cloudflare cache status `MISS`
- First request to edge is expected MISS
- Subsequent same URL should move toward HIT

3. Hidden image reappears
- Verify `ingest_blocklist` has the corresponding key

4. Pixiv link ingest fails
- Recheck `PIXIV_PHPSESSID` validity

## Security

- Never expose `BOT_TOKEN` in logs/screenshots.
- If leaked, regenerate token immediately in BotFather and update env.

