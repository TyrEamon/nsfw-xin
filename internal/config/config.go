package config

import (
	"log"
	"os"
	"strconv"
)

type Config struct {
	BotToken             string
	ChannelID            int64
	AdminPassword        string
	PixivPHPSESSID       string
	PixivUserID          string
	PixivTag             string
	PixivLimit           int
	PixivMaxPages        int
	PixivIntervalMinutes int
	D1AccountID          string
	D1ApiToken           string
	D1DatabaseID         string
	ListenAddr           string
}

func Load() *Config {
	cfg := &Config{
		BotToken:             os.Getenv("BOT_TOKEN"),
		AdminPassword:        os.Getenv("ADMIN_PASSWORD"),
		PixivPHPSESSID:       os.Getenv("PIXIV_PHPSESSID"),
		PixivUserID:          os.Getenv("PIXIV_USER_ID"),
		PixivTag:             os.Getenv("PIXIV_TAG"),
		PixivLimit:           getEnvInt("PIXIV_LIMIT", 40),
		PixivMaxPages:        getEnvInt("PIXIV_MAX_PAGES", 0),
		PixivIntervalMinutes: getEnvInt("PIXIV_INTERVAL_MINUTES", 120),
		D1AccountID:          os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		D1ApiToken:           os.Getenv("CLOUDFLARE_API_TOKEN"),
		D1DatabaseID:         os.Getenv("D1_DATABASE_ID"),
		ListenAddr:           getEnvString("LISTEN_ADDR", ":8080"),
	}

	channelStr := os.Getenv("CHANNEL_ID")
	if channelStr != "" {
		if id, err := strconv.ParseInt(channelStr, 10, 64); err == nil {
			cfg.ChannelID = id
		} else {
			log.Printf("invalid CHANNEL_ID: %v", err)
		}
	}

	return cfg
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
