package imageproc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/quota"
	"github.com/getsettalk/Tijori/internal/storage"
)

// Handler serves /img/<bucket>/<key>.
type Handler struct {
	Store    *storage.Store
	Verifier *auth.Verifier
	Quota    *quota.Manager
	Cache    *Cache
	Limits   Limits
	Log      *slog.Logger

	once  sync.Once
	sem   chan struct{}
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	res Result
	err error
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.once.Do(func() {
		h.sem = make(chan struct{}, max(runtime.NumCPU(), 2))
		h.calls = map[string]*call{}
	})
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		jsonError(w, http.StatusMethodNotAllowed, "only GET and HEAD are allowed")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/img/"), "/")
	if bucket == "" || key == "" {
		jsonError(w, http.StatusNotFound, "use /img/<bucket>/<key>")
		return
	}

	res, err := h.Verifier.Verify(r)
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	public := h.Quota.Settings(bucket).PublicRead
	if k := res.Key; k == nil {
		if !public {
			jsonError(w, http.StatusForbidden, "this bucket is private; use a presigned /img/ URL or make the bucket public")
			return
		}
	} else if !k.CanAccess(bucket) || !k.CanRead() {
		jsonError(w, http.StatusForbidden, "this key cannot read bucket "+bucket)
		return
	}
	h.ServeImage(w, r, bucket, key, public)
}

// ServeImage renders bucket/key using the request's query parameters. It
// does no authorisation; callers must have checked access already.
func (h *Handler) ServeImage(w http.ResponseWriter, r *http.Request, bucket, key string, public bool) {
	h.once.Do(func() {
		h.sem = make(chan struct{}, max(runtime.NumCPU(), 2))
		h.calls = map[string]*call{}
	})
	p, err := ParseParams(r.URL.Query(), h.Limits)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := h.Store.StatObject(bucket, key)
	if err != nil {
		if errors.Is(err, storage.ErrNoSuchBucket) || errors.Is(err, storage.ErrNoSuchKey) {
			jsonError(w, http.StatusNotFound, "image not found")
		} else {
			jsonError(w, http.StatusInternalServerError, "could not read image")
		}
		return
	}
	if h.Limits.MaxInputSize > 0 && info.Size > h.Limits.MaxInputSize {
		jsonError(w, http.StatusRequestEntityTooLarge, "original is "+config.FormatBytes(info.Size)+", over the resize limit of "+config.FormatBytes(h.Limits.MaxInputSize))
		return
	}

	sum := sha256.Sum256([]byte(bucket + "\x00" + key + "\x00" + info.ETag + "\x00" + p.String()))
	name := hex.EncodeToString(sum[:])
	etag := `"` + name[:32] + `"`
	cacheControl := "public, max-age=31536000"
	if !public {
		cacheControl = "private, max-age=86400"
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, strings.Trim(etag, `"`)) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", cacheControl)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	var out Result
	cacheState := "HIT"
	if data, ok := h.Cache.Get(name); ok {
		out = Result{Data: data, ContentType: http.DetectContentType(data)}
	} else {
		cacheState = "MISS"
		out, err = h.render(name, bucket, key, p)
		if err != nil {
			switch {
			case errors.Is(err, ErrUnsupported):
				jsonError(w, http.StatusUnsupportedMediaType, err.Error())
			case errors.Is(err, ErrTooManyPixels):
				jsonError(w, http.StatusUnprocessableEntity, err.Error())
			default:
				h.Log.Error("image processing failed", "bucket", bucket, "key", key, "err", err)
				jsonError(w, http.StatusInternalServerError, "image processing failed")
			}
			return
		}
	}
	hd := w.Header()
	hd.Set("Content-Type", out.ContentType)
	hd.Set("Content-Length", strconv.Itoa(len(out.Data)))
	hd.Set("Cache-Control", cacheControl)
	hd.Set("ETag", etag)
	hd.Set("X-Tijori-Cache", cacheState)
	if p.Format == "webp" {
		hd.Set("X-Tijori-Note", "webp output needs CGO; served "+out.ContentType+" instead")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(out.Data)
	}
}

// render produces an image once even if many requests ask at the same time.
func (h *Handler) render(name, bucket, key string, p Params) (Result, error) {
	h.mu.Lock()
	if c, ok := h.calls[name]; ok {
		h.mu.Unlock()
		c.wg.Wait()
		return c.res, c.err
	}
	c := &call{}
	c.wg.Add(1)
	h.calls[name] = c
	h.mu.Unlock()

	c.res, c.err = h.renderNow(bucket, key, p)
	if c.err == nil {
		h.Cache.Put(name, c.res.Data)
	}
	c.wg.Done()
	h.mu.Lock()
	delete(h.calls, name)
	h.mu.Unlock()
	return c.res, c.err
}

func (h *Handler) renderNow(bucket, key string, p Params) (Result, error) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()
	f, _, err := h.Store.GetObject(bucket, key)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	limit := h.Limits.MaxInputSize
	if limit <= 0 {
		limit = 100 << 20
	}
	src, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return Result{}, err
	}
	return Process(src, p, h.Limits)
}
