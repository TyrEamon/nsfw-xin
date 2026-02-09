# Pixiv + Telegram Gallery (Go)

## Features
- Pixiv 收藏抓取：原图 + 压缩预览 → TG 频道 → 写入 D1
- TG 私聊/转发图片：自动入库
- TG 消息链接抓取：支持 Pixiv artwork 链接与 yande.re post 链接自动入库
- 管理后台上传：浏览器上传 → TG 频道 → D1
- 前端瀑布流：基于提供的 gallery 样式

## Environment
Required:
- BOT_TOKEN
- CHANNEL_ID
- CLOUDFLARE_ACCOUNT_ID
- CLOUDFLARE_API_TOKEN
- D1_DATABASE_ID
- ADMIN_PASSWORD

Pixiv:
- PIXIV_PHPSESSID
- PIXIV_USER_ID
- PIXIV_TAG (optional; empty = all)
- PIXIV_REST (optional; `show` = public bookmarks, `hide` = private bookmarks)
- PIXIV_CRAWL_ORDER (optional; `desc` = newest to oldest, `asc` = oldest to newest)
- PIXIV_LIMIT (default: 40)
- PIXIV_MAX_PAGES (default: 0 = no limit)
- PIXIV_BOOTSTRAP_MAX_PAGES (default: inherit `PIXIV_MAX_PAGES`; `0` = no limit)
- PIXIV_INCREMENTAL_MAX_PAGES (default: 2, used after bootstrap done)
- PIXIV_INTERVAL_MINUTES (default: 120)

Server:
- LISTEN_ADDR (default: :8080)

## Run
```
go run ./cmd/server
```

## D1 Schema
See `schema.sql`.

## GitHub Image Workflow
- Workflow file: `.github/workflows/docker-image.yml`
- Trigger:
  - Push to `main`
  - Push tag like `v1.0.0`
  - Manual run (`workflow_dispatch`)
- Registry: `ghcr.io`
- Image: `ghcr.io/<owner>/<repo>`
  - Note: workflow normalizes `owner/repo` to lowercase for GHCR compatibility.
- Tags:
  - `main` branch tag
  - Git tag
  - Commit SHA
  - `latest` (default branch only)

## GitHub Settings
- In repository settings, keep Actions enabled.
- If your org restricts package publish, allow this repo to publish to GHCR.
- `GITHUB_TOKEN` is used automatically by the workflow for GHCR push.
