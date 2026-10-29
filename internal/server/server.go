// Package server wires Tijori's components together and runs the HTTP
// listeners.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/getsettalk/Tijori/internal/admin"
	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/backup"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/imageproc"
	"github.com/getsettalk/Tijori/internal/quota"
	"github.com/getsettalk/Tijori/internal/s3api"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
	"github.com/getsettalk/Tijori/internal/version"
	"github.com/getsettalk/Tijori/internal/video"
)

// Server is a running Tijori instance.
type Server struct {
	Cfg   *config.Config
	Log   *slog.Logger
	DB    *state.DB
	Store *storage.Store
	Quota *quota.Manager
	S3    *s3api.Handler

	// Optional components; nil when disabled.
	Images     http.Handler
	ImageCache *imageproc.Cache
	Admin      http.Handler
	AdminPanel *admin.Handler
	Backups    *backup.Runner
	Video      *video.Manager

	started  time.Time
	bg       sync.WaitGroup
	stop     chan struct{}
	stopOnce sync.Once
	closers  []func()
}

// NewLogger builds the slog logger described by cfg.
func NewLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Open prepares the data directory, database and storage. It is shared by
// the server and CLI commands that work offline.
func Open(cfg *config.Config) (*state.DB, *storage.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("cannot create data directory %s: %w", cfg.DataDir, err)
	}
	db, err := state.Open(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	store, err := storage.Open(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	return db, store, nil
}

// New builds a server from configuration.
func New(cfg *config.Config, log *slog.Logger) (*Server, error) {
	db, store, err := Open(cfg)
	if err != nil {
		return nil, err
	}
	db.SetEnvKey(cfg.AccessKey, cfg.SecretKey)
	if cfg.Admin.Password != "" {
		if err := EnsureAdminPassword(db, "admin", cfg.Admin.Password); err != nil {
			return nil, err
		}
	}
	for name := range cfg.Buckets {
		if !store.BucketExists(name) {
			if err := store.CreateBucket(name); err != nil {
				return nil, fmt.Errorf("creating bucket %s from config: %w", name, err)
			}
			log.Info("created bucket from config", "bucket", name)
		}
	}
	q := quota.New(cfg, store, db)
	t0 := time.Now()
	if _, err := q.Recalculate(); err != nil {
		return nil, fmt.Errorf("computing usage: %w", err)
	}
	log.Debug("usage recalculated", "took", time.Since(t0).Round(time.Millisecond))

	s := &Server{
		Cfg: cfg, Log: log, DB: db, Store: store, Quota: q,
		stop: make(chan struct{}),
		S3: &s3api.Handler{
			Store:       store,
			Verifier:    &auth.Verifier{Keys: db},
			Quota:       q,
			Region:      cfg.Region,
			ErrorFormat: cfg.ErrorFormat,
			Log:         log,
		},
	}
	if err := s.initOptional(); err != nil {
		return nil, err
	}
	return s, nil
}

// EnsureAdminPassword creates or updates an admin user's password.
func EnsureAdminPassword(db *state.DB, user, password string) error {
	if len(password) < 8 {
		return errors.New("admin password must be at least 8 characters")
	}
	if u, ok := db.User(user); ok && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u, ok := db.User(user)
	if !ok {
		u = state.AdminUser{Username: user, Role: "admin", CreatedAt: time.Now().UTC()}
	}
	u.PasswordHash = string(hash)
	return db.PutUser(u)
}

// ServeHTTP dispatches between health, metrics, admin, images and S3.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/health":
		s.health(w, r)
	case p == "/metrics" && s.Cfg.Metrics:
		s.metrics(w, r)
	case s.Admin != nil && s.Cfg.Admin.Listen == "" && (p == s.Cfg.Admin.Path || strings.HasPrefix(p, s.Cfg.Admin.Path+"/")):
		s.Admin.ServeHTTP(w, r)
	case s.Images != nil && strings.HasPrefix(p, "/img/"):
		s.Images.ServeHTTP(w, r)
	default:
		s.S3.ServeHTTP(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := os.Stat(s.Cfg.DataDir); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"error","error":"data directory not accessible"}`)
		return
	}
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	st := &s.S3.Stats
	fmt.Fprintf(w, "# HELP tijori_info Build information.\n# TYPE tijori_info gauge\ntijori_info{version=%q} 1\n", version.Version)
	fmt.Fprintf(w, "# TYPE tijori_uptime_seconds gauge\ntijori_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# TYPE tijori_s3_requests_total counter\ntijori_s3_requests_total %d\n", st.Requests.Load())
	fmt.Fprintf(w, "# TYPE tijori_s3_errors_total counter\ntijori_s3_errors_total %d\n", st.Errors.Load())
	fmt.Fprintf(w, "# TYPE tijori_s3_uploads_total counter\ntijori_s3_uploads_total %d\n", st.Uploads.Load())
	fmt.Fprintf(w, "# TYPE tijori_s3_downloads_total counter\ntijori_s3_downloads_total %d\n", st.Downloads.Load())
	fmt.Fprintf(w, "# TYPE tijori_s3_received_bytes_total counter\ntijori_s3_received_bytes_total %d\n", st.BytesIn.Load())
	fmt.Fprintf(w, "# TYPE tijori_s3_sent_bytes_total counter\ntijori_s3_sent_bytes_total %d\n", st.BytesOut.Load())
	fmt.Fprintf(w, "# TYPE tijori_bucket_bytes gauge\n")
	u := s.Quota.Usage()
	for _, b := range state.SortedBucketNames(u) {
		fmt.Fprintf(w, "tijori_bucket_bytes{bucket=%q} %d\n", b, u[b].Bytes)
	}
	fmt.Fprintf(w, "# TYPE tijori_bucket_objects gauge\n")
	for _, b := range state.SortedBucketNames(u) {
		fmt.Fprintf(w, "tijori_bucket_objects{bucket=%q} %d\n", b, u[b].Objects)
	}
}

// statusRecorder captures the response status and size for access logs.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := io.Copy(r.ResponseWriter, src)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// withAccessLog logs every request. Query strings are never logged because
// presigned URLs carry signatures in them.
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("panic serving request", "method", r.Method, "path", r.URL.Path, "panic", v)
				if rec.status == 0 {
					http.Error(rec, "internal error", http.StatusInternalServerError)
				}
			}
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			lvl := slog.LevelInfo
			if r.URL.Path == "/health" || r.URL.Path == "/metrics" {
				lvl = slog.LevelDebug
			}
			s.Log.Log(r.Context(), lvl, "request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"bytes", rec.bytes, "ms", time.Since(t0).Milliseconds(), "remote", remoteIP(r))
		}()
		next.ServeHTTP(rec, r)
	})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Handler returns the main HTTP handler with logging.
func (s *Server) Handler() http.Handler { return s.withAccessLog(s) }

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
}

// Start begins background jobs. Run calls it; tests may call it directly.
func (s *Server) Start() {
	s.started = time.Now()
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		flush := time.NewTicker(15 * time.Second)
		clean := time.NewTicker(time.Hour)
		defer flush.Stop()
		defer clean.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-flush.C:
				if err := s.DB.Flush(); err != nil {
					s.Log.Error("saving usage counters", "err", err)
				}
			case <-clean.C:
				if n := s.Store.CleanStaleUploads(7 * 24 * time.Hour); n > 0 {
					s.Log.Info("removed stale multipart uploads", "count", n)
				}
			}
		}
	}()
	s.startOptional()
}

// Close stops background jobs and flushes state.
func (s *Server) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		for i := len(s.closers) - 1; i >= 0; i-- {
			s.closers[i]()
		}
		s.bg.Wait()
		if err := s.DB.Flush(); err != nil {
			s.Log.Error("saving usage counters", "err", err)
		}
	})
}

// Run listens until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	main := newHTTPServer(s.Cfg.Listen, s.Handler())
	servers := []*http.Server{main}
	if s.Admin != nil && s.Cfg.Admin.Listen != "" {
		mux := http.NewServeMux()
		mux.Handle(s.Cfg.Admin.Path+"/", s.Admin)
		mux.Handle(s.Cfg.Admin.Path, s.Admin)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, s.Cfg.Admin.Path+"/", http.StatusFound)
		})
		servers = append(servers, newHTTPServer(s.Cfg.Admin.Listen, s.withAccessLog(mux)))
	}
	listeners := make([]net.Listener, len(servers))
	for i, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			for _, l := range listeners[:i] {
				l.Close()
			}
			return fmt.Errorf("cannot listen on %s: %w (is another program using this port?)", srv.Addr, err)
		}
		listeners[i] = ln
	}
	s.Start()
	defer s.Close()
	s.banner()

	errc := make(chan error, len(servers))
	for i, srv := range servers {
		go func(srv *http.Server, ln net.Listener) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}(srv, listeners[i])
	}
	select {
	case <-ctx.Done():
		s.Log.Info("shutting down")
	case err := <-errc:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(sctx)
	}
	return nil
}

func (s *Server) banner() {
	for _, w := range s.Cfg.Warnings() {
		s.Log.Warn(w)
	}
	base := s.Cfg.PublicURL
	if base == "" {
		host, port, _ := net.SplitHostPort(s.Cfg.Listen)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		base = "http://" + net.JoinHostPort(host, port)
	}
	cfgFile := s.Cfg.File
	if cfgFile == "" {
		cfgFile = "(none, using defaults and environment)"
	}
	s.Log.Info("Tijori started", "version", version.Version, "listen", s.Cfg.Listen, "data_dir", s.Cfg.DataDir, "config", cfgFile)
	s.Log.Info("S3 endpoint ready", "url", base)
	if s.Admin != nil {
		adminURL := strings.TrimSuffix(base, "/") + s.Cfg.Admin.Path + "/"
		if s.Cfg.Admin.Listen != "" {
			adminURL = "http://" + s.Cfg.Admin.Listen + s.Cfg.Admin.Path + "/"
		}
		s.Log.Info("admin panel ready", "url", adminURL)
		if len(s.DB.Users()) == 0 {
			s.Log.Warn("no admin user yet: run `tijori init` or set TIJORI_ADMIN_PASSWORD to log in to the admin panel")
		}
	}
	if len(s.DB.Keys()) == 0 {
		s.Log.Warn("no access keys yet: run `tijori key create` or create one in the admin panel")
	}
}
