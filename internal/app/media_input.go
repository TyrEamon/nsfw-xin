package app

import (
	"fmt"
	"path"
	"strings"

	"github.com/go-telegram/bot/models"
)

type incomingMediaKind string

const (
	incomingMediaImage     incomingMediaKind = "image"
	incomingMediaVideo     incomingMediaKind = "video"
	incomingMediaAnimation incomingMediaKind = "animation"
)

type incomingMedia struct {
	FileID   string
	Kind     incomingMediaKind
	Filename string
}

func (m incomingMedia) isImage() bool {
	return m.Kind == incomingMediaImage
}

func (m incomingMedia) isAnimation() bool {
	return m.Kind == incomingMediaAnimation
}

func (m incomingMedia) displayName() string {
	switch m.Kind {
	case incomingMediaAnimation:
		return "??"
	case incomingMediaVideo:
		return "??"
	default:
		return "??"
	}
}

func extractIncomingMedia(msg *models.Message) (incomingMedia, bool) {
	if msg == nil {
		return incomingMedia{}, false
	}

	if len(msg.Photo) > 0 {
		return incomingMedia{FileID: strings.TrimSpace(msg.Photo[len(msg.Photo)-1].FileID), Kind: incomingMediaImage, Filename: ""}, true
	}
	if msg.Video != nil {
		return incomingMedia{
			FileID:   strings.TrimSpace(msg.Video.FileID),
			Kind:     incomingMediaVideo,
			Filename: pickFilename(msg.Video.FileName, "tg_video", ".mp4"),
		}, true
	}
	if msg.Animation != nil {
		return incomingMedia{
			FileID:   strings.TrimSpace(msg.Animation.FileID),
			Kind:     incomingMediaAnimation,
			Filename: pickFilename(msg.Animation.FileName, "tg_animation", ".mp4"),
		}, true
	}
	if msg.Document != nil {
		kind := classifyDocumentKind(msg.Document)
		defExt := ".jpg"
		switch kind {
		case incomingMediaVideo:
			defExt = ".mp4"
		case incomingMediaAnimation:
			defExt = ".mp4"
		}
		return incomingMedia{
			FileID:   strings.TrimSpace(msg.Document.FileID),
			Kind:     kind,
			Filename: pickFilename(msg.Document.FileName, "tg_document", defExt),
		}, true
	}

	return incomingMedia{}, false
}

func classifyDocumentKind(doc *models.Document) incomingMediaKind {
	if doc == nil {
		return incomingMediaImage
	}
	mime := strings.ToLower(strings.TrimSpace(doc.MimeType))
	name := strings.ToLower(strings.TrimSpace(doc.FileName))
	ext := strings.ToLower(path.Ext(name))

	if mime == "image/gif" || ext == ".gif" {
		return incomingMediaAnimation
	}
	if strings.HasPrefix(mime, "video/") {
		return incomingMediaVideo
	}
	switch ext {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv":
		return incomingMediaVideo
	}
	return incomingMediaImage
}

func pickFilename(name, prefix, defExt string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if defExt == "" {
		defExt = ".bin"
	}
	if prefix == "" {
		prefix = "file"
	}
	if !strings.HasPrefix(defExt, ".") {
		defExt = "." + defExt
	}
	return fmt.Sprintf("%s%s", prefix, defExt)
}
