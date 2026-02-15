package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/nfnt/resize"
)

const (
	maxPhotoSize    = 9 * 1024 * 1024
	maxDimension    = 4950
	minJPEGQuality  = 50
	getFileRetries  = 3
	downloadRetries = 3
	retryBaseDelay  = 200 * time.Millisecond
)

type Client struct {
	Bot               *bot.Bot
	Token             string
	PublishChannelID  int64
	StorageChannelID  int64
	DiscussionGroupID int64
}

type SendOptions struct {
	PreviewCaption string
	OriginCaption  string
}

type SendResult struct {
	PreviewID       string
	OriginID        string
	PublishMsgID    int
	StorageMsgID    int
	DiscussionMsgID int
	Width           int
	Height          int
}

type DiscussionButtons struct {
	DetailsURL string
	OriginURL  string
}

func New(token string, publishChannelID, storageChannelID, discussionGroupID int64) (*Client, error) {
	b, err := bot.New(token)
	if err != nil {
		return nil, err
	}
	return &Client{
		Bot:               b,
		Token:             token,
		PublishChannelID:  publishChannelID,
		StorageChannelID:  storageChannelID,
		DiscussionGroupID: discussionGroupID,
	}, nil
}

func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	var (
		file *models.File
		err  error
	)

	for attempt := 1; attempt <= getFileRetries; attempt++ {
		file, err = c.Bot.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
		if err == nil {
			break
		}
		if attempt == getFileRetries || ctx.Err() != nil {
			return nil, "", err
		}
		if sleepErr := sleepRetry(ctx, attempt); sleepErr != nil {
			return nil, "", sleepErr
		}
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.Token, file.FilePath)
	client := &http.Client{Timeout: 25 * time.Second}
	var lastErr error

	for attempt := 1; attempt <= downloadRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if reqErr != nil {
			return nil, "", reqErr
		}

		resp, httpErr := client.Do(req)
		if httpErr != nil {
			lastErr = httpErr
		} else {
			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				return data, file.FilePath, nil
			}
			if readErr != nil {
				lastErr = readErr
			} else {
				lastErr = fmt.Errorf("telegram file download status: %d", resp.StatusCode)
			}
			if !shouldRetryStatus(resp.StatusCode) {
				return nil, "", lastErr
			}
		}

		if attempt == downloadRetries || ctx.Err() != nil {
			break
		}
		if !shouldRetryError(lastErr) {
			break
		}
		if sleepErr := sleepRetry(ctx, attempt); sleepErr != nil {
			return nil, "", sleepErr
		}
	}

	return nil, "", lastErr
}

func (c *Client) SendArtwork(ctx context.Context, data []byte, opts SendOptions) (SendResult, error) {
	res := SendResult{}
	res.Width, res.Height, _ = getImageInfo(data)

	previewData := data
	if needsCompress(data, res.Width, res.Height) {
		if compressed, err := compressImage(data, maxPhotoSize); err == nil {
			previewData = compressed
		}
	}

	publishMsg, err := c.Bot.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:    c.PublishChannelID,
		Photo:     &models.InputFileUpload{Filename: "preview.jpg", Data: bytes.NewReader(previewData)},
		Caption:   opts.PreviewCaption,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		return SendResult{}, err
	}
	res.PublishMsgID = publishMsg.ID
	if len(publishMsg.Photo) > 0 {
		res.PreviewID = publishMsg.Photo[len(publishMsg.Photo)-1].FileID
	}

	originCaption := opts.OriginCaption
	if originCaption == "" {
		originCaption = "Original"
	}
	_, format := detectImageFormat(data)
	originName := "origin." + extFromFormat(format)
	storageMsg, err := c.Bot.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   c.StorageChannelID,
		Document: &models.InputFileUpload{Filename: originName, Data: bytes.NewReader(data)},
		Caption:  originCaption,
	})
	if err != nil {
		return SendResult{}, err
	}
	res.StorageMsgID = storageMsg.ID
	if storageMsg.Document != nil {
		res.OriginID = storageMsg.Document.FileID
	}

	return res, nil
}

func (c *Client) SendDiscussionComment(ctx context.Context, publishMessageID int, text string, buttons DiscussionButtons) (int, error) {
	disablePreview := true
	replyMarkup := buildDiscussionReplyMarkup(buttons)

	msg, err := c.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          c.DiscussionGroupID,
		MessageThreadID: publishMessageID,
		Text:            text,
		ReplyMarkup:     replyMarkup,
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: &disablePreview,
		},
	})
	if err == nil {
		return msg.ID, nil
	}

	fallback, fallbackErr := c.Bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      c.DiscussionGroupID,
		Text:        text,
		ReplyMarkup: replyMarkup,
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: &disablePreview,
		},
	})
	if fallbackErr != nil {
		return 0, err
	}
	return fallback.ID, err
}

func buildDiscussionReplyMarkup(buttons DiscussionButtons) *models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, 1)
	row := make([]models.InlineKeyboardButton, 0, 2)
	if buttons.DetailsURL != "" {
		row = append(row, models.InlineKeyboardButton{Text: "\u8be6\u60c5", URL: buttons.DetailsURL})
	}
	if buttons.OriginURL != "" {
		row = append(row, models.InlineKeyboardButton{Text: "\u539f\u56fe", URL: buttons.OriginURL})
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func detectImageFormat(data []byte) (image.Config, string) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, ""
	}
	return cfg, format
}

func (c *Client) SendPreviewAndOrigin(ctx context.Context, data []byte, caption string) (previewID, originID string, previewMsgID, originMsgID int, width, height int, err error) {
	res, err := c.SendArtwork(ctx, data, SendOptions{
		PreviewCaption: caption,
		OriginCaption:  "Original",
	})
	if err != nil {
		return "", "", 0, 0, 0, 0, err
	}
	return res.PreviewID, res.OriginID, res.PublishMsgID, res.StorageMsgID, res.Width, res.Height, nil
}

func getImageInfo(data []byte) (int, int, string) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, ""
	}
	return cfg.Width, cfg.Height, format
}

func extFromFormat(format string) string {
	switch format {
	case "jpeg":
		return "jpg"
	case "png":
		return "png"
	case "gif":
		return "gif"
	case "webp":
		return "webp"
	default:
		return "bin"
	}
}

func needsCompress(data []byte, width, height int) bool {
	if int64(len(data)) > maxPhotoSize {
		return true
	}
	if width > maxDimension || height > maxDimension {
		return true
	}
	return false
}

func compressImage(data []byte, targetSize int64) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width > maxDimension || height > maxDimension {
		if width > height {
			img = resize.Resize(maxDimension, 0, img, resize.Lanczos3)
		} else {
			img = resize.Resize(0, maxDimension, img, resize.Lanczos3)
		}
	}

	quality := 95
	for {
		buf := new(bytes.Buffer)
		err = jpeg.Encode(buf, img, &jpeg.Options{Quality: quality})
		if err != nil {
			return nil, err
		}
		if int64(buf.Len()) <= targetSize || quality <= minJPEGQuality {
			return buf.Bytes(), nil
		}
		quality -= 3
	}
}

func shouldRetryStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func shouldRetryError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return true
}

func sleepRetry(ctx context.Context, attempt int) error {
	delay := retryBaseDelay * time.Duration(1<<uint(attempt-1))
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) Start(ctx context.Context) {
	c.Bot.Start(ctx)
}

func (c *Client) Stop() {}
