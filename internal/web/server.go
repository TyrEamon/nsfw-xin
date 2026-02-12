package web

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pixiv-tg-gallery/internal/app"
	"pixiv-tg-gallery/internal/config"
	"pixiv-tg-gallery/internal/database"
	"pixiv-tg-gallery/internal/telegram"
	"pixiv-tg-gallery/internal/umami"
)

type Server struct {
	cfg   *config.Config
	db    *database.Client
	tg    *telegram.Client
	app   *app.App
	umami *umami.Client
}

func New(cfg *config.Config, db *database.Client, tg *telegram.Client, app *app.App, um *umami.Client) *Server {
	return &Server{cfg: cfg, db: db, tg: tg, app: app, umami: um}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/gallery", s.handleGallery)
	mux.HandleFunc("/favorites", s.handleFavorites)
	mux.HandleFunc("/gallery.js", s.handleGalleryJS)
	mux.HandleFunc("/api/posts", s.handleApiPosts)
	mux.HandleFunc("/api/favorites", s.handleApiFavorites)
	mux.HandleFunc("/api/random", s.handleApiRandom)
	mux.HandleFunc("/image/", s.handleImageProxy)

	mux.HandleFunc("/admin", s.withAdminAuth(s.handleAdminRoot))
	mux.HandleFunc("/admin/upload", s.withAdminAuth(s.handleAdminUpload))
	mux.HandleFunc("/admin/api/images", s.withAdminAuth(s.handleAdminApiImages))
	mux.HandleFunc("/admin/api/images/hide", s.withAdminAuth(s.handleAdminApiHideImage))
	mux.HandleFunc("/admin/api/images/favorite", s.withAdminAuth(s.handleAdminApiFavorite))
	mux.HandleFunc("/admin/api/umami/summary", s.withAdminAuth(s.handleAdminApiUmamiSummary))

	mux.Handle("/lib/", http.FileServer(http.Dir(filepath.Join("web"))))
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/gallery", http.StatusFound)
}

func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, filepath.Join("web", "gallery.html"))
}

func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, filepath.Join("web", "favorites.html"))
}

func (s *Server) handleGalleryJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	s.serveFile(w, r, filepath.Join("web", "gallery.js"))
}

func (s *Server) handleAdminRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/upload", http.StatusFound)
}

func (s *Server) handleAdminUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.serveFile(w, r, filepath.Join("web", "admin", "upload.html"))
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no files"})
		return
	}

	success := 0
	failed := 0

	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			failed++
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			failed++
			continue
		}

		if err := s.app.HandleUpload(r.Context(), data); err != nil {
			failed++
			continue
		}
		success++
	}

	writeJSON(w, http.StatusOK, map[string]int{"success": success, "failed": failed})
}

func (s *Server) handleApiPosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	orientation := q.Get("type")

	results, err := s.db.ListImages(r.Context(), offset, limit, orientation)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(results)
}

func (s *Server) handleApiFavorites(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	orientation := q.Get("type")

	results, err := s.db.ListFavorites(r.Context(), offset, limit, orientation)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(results)
}

func (s *Server) handleApiRandom(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	orientation := r.URL.Query().Get("type")
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	item, err := s.db.RandomImage(r.Context(), orientation)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if item == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no image"})
		return
	}

	previewPath := ""
	if previewID, ok := item["preview_id"].(string); ok && previewID != "" {
		previewPath = fmt.Sprintf("/image/%s", previewID)
		item["preview_url"] = previewPath
	}

	if previewPath == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}

	if format == "url" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(buildAbsoluteURL(r, previewPath)))
		return
	}

	if format == "redirect" {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, previewPath, http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Random API intentionally exposes preview only.
	delete(item, "origin_id")

	json.NewEncoder(w).Encode(item)
}

func buildAbsoluteURL(r *http.Request, path string) string {
	scheme := "https"
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.TrimSpace(strings.Split(proto, ",")[0])
	} else if r.TLS == nil {
		scheme = "http"
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return path
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

func (s *Server) handleAdminApiImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	status := q.Get("status")

	items, err := s.db.ListAdminImages(r.Context(), offset, limit, status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleAdminApiHideImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	var req struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "admin_hide"
	}

	if err := s.db.HideAndBlock(r.Context(), req.ID, req.Reason); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminApiFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	var req struct {
		ID string `json:"id"`
		On *bool  `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}

	on := true
	if req.On != nil {
		on = *req.On
	}

	if err := s.db.SetFavorite(r.Context(), req.ID, on); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminApiUmamiSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.umami == nil || !s.umami.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "umami not configured"})
		return
	}

	summary, err := s.umami.GetSummary(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleImageProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	fileID := strings.TrimPrefix(r.URL.Path, "/image/")
	if fileID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	data, name, err := s.tg.DownloadFile(r.Context(), fileID)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	if ext := filepath.Ext(name); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			w.Header().Set("Content-Type", t)
		}
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	if r.URL.Query().Get("dl") != "" {
		filename := fileID
		if name != "" {
			filename = filepath.Base(name)
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	}

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer f.Close()

	http.ServeContent(w, r, path, getFileModTime(f), f)
}

func getFileModTime(f *os.File) (t time.Time) {
	if info, err := f.Stat(); err == nil {
		return info.ModTime()
	}
	return time.Now()
}

func (s *Server) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminPassword == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
		_ = user
		if !ok || pass != s.cfg.AdminPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func parseBasicAuth(header string) (string, string, bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
