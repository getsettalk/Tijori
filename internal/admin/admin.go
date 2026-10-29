// Package admin serves the embedded admin panel and its JSON API.
package admin

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/imageproc"
	"github.com/getsettalk/Tijori/internal/quota"
	"github.com/getsettalk/Tijori/internal/s3api"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
	"github.com/getsettalk/Tijori/web"
)

// VideoInfo describes video processing for the dashboard.
type VideoInfo struct {
	Available bool   `json:"available"`
	FFmpeg    string `json:"ffmpeg"`
	Version   string `json:"version"`
	Queued    int    `json:"queued"`
	Done      int64  `json:"done"`
	Failed    int64  `json:"failed"`
}

// Handler is the admin panel.
type Handler struct {
	Cfg     *config.Config
	DB      *state.DB
	Store   *storage.Store
	Quota   *quota.Manager
	S3      *s3api.Handler
	Images  *imageproc.Handler // nil when image processing is off
	Log     *slog.Logger
	Started time.Time

	// Optional hooks filled in by the server.
	Video         func() VideoInfo
	RunBackup     func() error
	BackupRunning func() bool
	ImageStats    func() (entries int, bytes, hits, misses int64)

	sessions sessions
	limiter  limiter
}

type ctxSession struct {
	id string
	*session
}

func (h *Handler) base() string { return h.Cfg.Admin.Path }

func securityHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "same-origin")
	hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, h.base())
	switch {
	case rest == "":
		http.Redirect(w, r, h.base()+"/", http.StatusFound)
	case strings.HasPrefix(rest, "/api/"):
		h.api(w, r, strings.TrimPrefix(rest, "/api"))
	case rest == "/" || rest == "/index.html":
		h.index(w, r)
	default:
		h.static(w, r, strings.TrimPrefix(rest, "/"))
	}
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(web.Admin, "admin/index.html")
	if err != nil {
		http.Error(w, "admin panel files missing from this build", http.StatusInternalServerError)
		return
	}
	b = bytes.ReplaceAll(b, []byte("{{BASE}}"), []byte(h.base()))
	securityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func (h *Handler) static(w http.ResponseWriter, r *http.Request, name string) {
	name = path.Clean("/" + name)[1:]
	b, err := fs.ReadFile(web.Admin, "admin/"+name)
	if err != nil || name == "" {
		http.NotFound(w, r)
		return
	}
	securityHeaders(w)
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(b)
}

// ---- JSON helpers -------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sameOrigin rejects cross-site requests that carry an Origin header from
// somewhere else (defence in depth on top of SameSite=Strict and CSRF).
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (h *Handler) currentSession(r *http.Request) (ctxSession, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ctxSession{}, false
	}
	se, ok := h.sessions.get(c.Value)
	if !ok {
		return ctxSession{}, false
	}
	// A user deleted or demoted from the CLI loses access right away.
	u, ok := h.DB.User(se.user)
	if !ok {
		h.sessions.delete(c.Value)
		return ctxSession{}, false
	}
	se.role = u.Role
	return ctxSession{c.Value, se}, true
}

// api routes /api/* requests after checking session, CSRF and role.
func (h *Handler) api(w http.ResponseWriter, r *http.Request, p string) {
	securityHeaders(w)
	if !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if p == "/login" && r.Method == http.MethodPost {
		h.login(w, r)
		return
	}
	se, ok := h.currentSession(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "please log in")
		return
	}
	mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
	if mutating && !equalConst(r.Header.Get("X-CSRF-Token"), se.csrf) {
		apiError(w, http.StatusForbidden, "invalid CSRF token; reload the page")
		return
	}
	if mutating && p != "/logout" && se.role == "readonly" {
		apiError(w, http.StatusForbidden, "your account is read-only")
		return
	}
	route := r.Method + " " + p
	switch {
	case route == "POST /logout":
		h.logout(w, r, se)
	case route == "GET /me":
		writeJSON(w, 200, map[string]any{"user": se.user, "role": se.role, "csrf": se.csrf})
	case route == "GET /overview":
		h.overview(w, r)
	case route == "GET /buckets":
		h.listBuckets(w, r)
	case route == "POST /buckets":
		h.createBucket(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/buckets/"):
		h.updateBucket(w, r, strings.TrimPrefix(p, "/buckets/"))
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/buckets/"):
		h.deleteBucket(w, r, strings.TrimPrefix(p, "/buckets/"))
	case route == "GET /objects":
		h.listObjects(w, r)
	case route == "PUT /object":
		h.upload(w, r)
	case route == "DELETE /object":
		h.deleteObject(w, r)
	case route == "POST /folder":
		h.createFolder(w, r)
	case route == "GET /file":
		h.download(w, r)
	case route == "GET /thumb":
		h.thumb(w, r)
	case route == "POST /presign":
		h.presign(w, r)
	case route == "GET /keys":
		h.listKeys(w, r, se)
	case route == "POST /keys":
		h.createKey(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/keys/"):
		h.revokeKey(w, r, strings.TrimPrefix(p, "/keys/"))
	case route == "GET /backup":
		h.backupStatus(w, r)
	case route == "POST /backup":
		h.runBackup(w, r)
	case route == "GET /settings":
		h.settings(w, r)
	case route == "POST /password":
		h.changePassword(w, r, se)
	default:
		apiError(w, http.StatusNotFound, "unknown API endpoint")
	}
}
