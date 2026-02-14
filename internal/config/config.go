package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken                 string
	ChannelID                int64
	AdminPassword            string
	BackupEnabled            bool
	BackupWebDAVURL          string
	BackupWebDAVUsername     string
	BackupWebDAVPassword     string
	BackupBasePath           string
	BackupWorkers            int
	BackupRetryMax           int
	BackupPollSeconds        int
	BackupTaskTimeoutSeconds int
	TwitterAPIDomain         string
	UmamiBaseURL             string
	UmamiWebsiteIDFrontend   string
	UmamiUsername            string
	UmamiPassword            string
	UmamiAPIToken            string
	UmamiLookbackDays        int
	PixivPHPSESSID           string
	PixivUserID              string
	PixivTag                 string
	PixivRest                string
	PixivCrawlOrder          string
	PixivLimit               int
	PixivMaxPages            int
	PixivBootstrapMaxPages   int
	PixivIncrementalMaxPages int
	PixivIntervalMinutes     int
	D1AccountID              string
	D1ApiToken               string
	D1DatabaseID             string
	ListenAddr               string
}

func Load() *Config {
	cfg := &Config{
		BotToken:                 os.Getenv("BOT_TOKEN"),
		AdminPassword:            os.Getenv("ADMIN_PASSWORD"),
		BackupEnabled:            getEnvBool("BACKUP_ENABLED", false),
		BackupWebDAVURL:          getEnvString("BACKUP_WEBDAV_URL", ""),
		BackupWebDAVUsername:     os.Getenv("BACKUP_WEBDAV_USERNAME"),
		BackupWebDAVPassword:     os.Getenv("BACKUP_WEBDAV_PASSWORD"),
		BackupBasePath:           getEnvString("BACKUP_BASE_PATH", "/MyPixiv"),
		BackupWorkers:            getEnvInt("BACKUP_WORKERS", 1),
		BackupRetryMax:           getEnvInt("BACKUP_RETRY_MAX", 5),
		BackupPollSeconds:        getEnvInt("BACKUP_POLL_SECONDS", 8),
		BackupTaskTimeoutSeconds: getEnvInt("BACKUP_TASK_TIMEOUT_SECONDS", 120),
		TwitterAPIDomain:         getEnvString("TWITTER_API_DOMAIN", "fxtwitter.com"),
		UmamiBaseURL:             getEnvString("UMAMI_BASE_URL", ""),
		UmamiWebsiteIDFrontend:   os.Getenv("UMAMI_WEBSITE_ID_FRONTEND"),
		UmamiUsername:            os.Getenv("UMAMI_USERNAME"),
		UmamiPassword:            os.Getenv("UMAMI_PASSWORD"),
		UmamiAPIToken:            os.Getenv("UMAMI_API_TOKEN"),
		UmamiLookbackDays:        getEnvInt("UMAMI_LOOKBACK_DAYS", 7),
		PixivPHPSESSID:           os.Getenv("PIXIV_PHPSESSID"),
		PixivUserID:              os.Getenv("PIXIV_USER_ID"),
		PixivTag:                 os.Getenv("PIXIV_TAG"),
		PixivRest:                getEnvString("PIXIV_REST", "show"),
		PixivCrawlOrder:          getEnvString("PIXIV_CRAWL_ORDER", "desc"),
		PixivLimit:               getEnvInt("PIXIV_LIMIT", 40),
		PixivMaxPages:            getEnvInt("PIXIV_MAX_PAGES", 0),
		PixivBootstrapMaxPages:   getEnvInt("PIXIV_BOOTSTRAP_MAX_PAGES", -1),
		PixivIncrementalMaxPages: getEnvInt("PIXIV_INCREMENTAL_MAX_PAGES", 2),
		PixivIntervalMinutes:     getEnvInt("PIXIV_INTERVAL_MINUTES", 120),
		D1AccountID:              os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		D1ApiToken:               os.Getenv("CLOUDFLARE_API_TOKEN"),
		D1DatabaseID:             os.Getenv("D1_DATABASE_ID"),
		ListenAddr:               getEnvString("LISTEN_ADDR", ":8080"),
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

func getEnvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
