# Pixiv + Telegram Gallery (Go)

## Features
- Pixiv 收藏抓取：原图 + 压缩预览 → TG 频道 → 写入 D1
- TG 私聊/转发图片：自动入库
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
- PIXIV_LIMIT (default: 40)
- PIXIV_MAX_PAGES (default: 0 = no limit)
- PIXIV_INTERVAL_MINUTES (default: 120)

Server:
- LISTEN_ADDR (default: :8080)

## Run
```
go run ./cmd/server
```

## D1 Schema
See `schema.sql`.
