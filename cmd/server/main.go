package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pixiv-tg-gallery/internal/app"
	"pixiv-tg-gallery/internal/config"
	"pixiv-tg-gallery/internal/database"
	"pixiv-tg-gallery/internal/pixiv"
	"pixiv-tg-gallery/internal/telegram"
	"pixiv-tg-gallery/internal/web"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func main() {
	cfg := config.Load()
	if cfg.BotToken == "" || cfg.ChannelID == 0 {
		log.Fatal("BOT_TOKEN or CHANNEL_ID missing")
	}
	if cfg.D1AccountID == "" || cfg.D1ApiToken == "" || cfg.D1DatabaseID == "" {
		log.Fatal("D1 credentials missing")
	}
	if cfg.AdminPassword == "" {
		log.Println("warning: ADMIN_PASSWORD is empty, /admin will be blocked")
	}

	db := database.New(cfg.D1AccountID, cfg.D1ApiToken, cfg.D1DatabaseID)
	tg, err := telegram.New(cfg.BotToken, cfg.ChannelID)
	if err != nil {
		log.Fatal(err)
	}

	pv := pixiv.New(cfg.PixivPHPSESSID, cfg.PixivUserID)
	application := app.New(cfg, db, tg, pv)

	tg.Bot.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		if update.Message == nil {
			return false
		}
		return len(update.Message.Photo) > 0 || update.Message.Document != nil
	}, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		result, err := application.HandleTGMessage(ctx, update.Message)
		if err != nil {
			log.Printf("tg handle error: %v", err)
			if update.Message != nil && update.Message.Chat.ID != cfg.ChannelID {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.Message.Chat.ID,
					Text:   fmt.Sprintf("笨蛋，这次处理失败了喵~\n错误：%v", err),
				})
			}
			return
		}
		if result != nil && update.Message != nil && update.Message.Chat.ID != cfg.ChannelID {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.Message.Chat.ID,
				Text: fmt.Sprintf(
					"哼，才不是特意帮你处理的喵~\n转发和录入都完成了。\n标题：%s\nID：%s",
					result.Title,
					result.ID,
				),
			})
		}
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application.StartPixivCrawler(ctx)

	mux := http.NewServeMux()
	server := web.New(cfg, db, tg, application)
	server.Register(mux)

	httpSrv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}

	go func() {
		log.Printf("HTTP server listening on %s", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	go tg.Start(ctx)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	tg.Stop()
	log.Println("shutdown complete")
}
