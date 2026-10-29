package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/s3api"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
	"github.com/getsettalk/Tijori/internal/sysinfo"
	"github.com/getsettalk/Tijori/internal/version"
)

// ---- login ----------------------------------------------------------------

var dummyHash = sync.OnceValue(func() []byte {
	b, _ := bcrypt.GenerateFromPassword([]byte("tijori-timing-equaliser"), bcrypt.DefaultCost)
	return b
})

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, "send username and password")
		return
	}
	ip := clientIP(r)
	_, known := h.DB.User(in.Username)
	limitUser := ""
	if known {
		limitUser = in.Username
	}
	if blocked, wait := h.limiter.blocked(ip, limitUser); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		apiError(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed logins; try again in %d minutes", int(wait.Minutes())+1))
		return
	}
	u, ok := h.DB.User(in.Username)
	hash := u.PasswordHash
	if !ok {
		// Spend the same time as a real check so user names can't be probed.
		hash = string(dummyHash())
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil || !ok {
		h.limiter.fail(ip, limitUser)
		h.Log.Warn("admin login failed", "user", in.Username, "remote", ip)
		apiError(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	h.limiter.reset(ip)
	id, se := h.sessions.create(u.Username, u.Role)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: id, Path: h.base(), HttpOnly: true, Secure: isHTTPS(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
	h.Log.Info("admin login", "user", u.Username, "remote", ip)
	writeJSON(w, 200, map[string]any{"user": se.user, "role": se.role, "csrf": se.csrf})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, se ctxSession) {
	h.sessions.delete(se.id)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: h.base(), MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, se ctxSession) {
	var in struct{ Current, New string }
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	u, ok := h.DB.User(se.user)
	if !ok || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		apiError(w, 403, "current password is wrong")
		return
	}
	if len(in.New) < 8 {
		apiError(w, 400, "new password must be at least 8 characters")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(in.New), bcrypt.DefaultCost)
	u.PasswordHash = string(hash)
	if err := h.DB.PutUser(u); err != nil {
		apiError(w, 500, err.Error())
		return
	}
	h.sessions.dropUser(se.user)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- dashboard ------------------------------------------------------------

type bucketView struct {
	Name          string `json:"name"`
	Created       string `json:"created"`
	Bytes         int64  `json:"bytes"`
	Objects       int64  `json:"objects"`
	PublicRead    bool   `json:"public_read"`
	MaxSize       int64  `json:"max_size"`
	MaxObjectSize int64  `json:"max_object_size"`
	VideoProcess  bool   `json:"video_process"`
	Managed       bool   `json:"managed"`
}

func (h *Handler) bucketViews() ([]bucketView, error) {
	buckets, err := h.Store.ListBuckets()
	if err != nil {
		return nil, err
	}
	usage := h.Quota.Usage()
	out := []bucketView{}
	for _, b := range buckets {
		st := h.Quota.Settings(b.Name)
		u := usage[b.Name]
		out = append(out, bucketView{Name: b.Name, Created: b.Created.Format(time.RFC3339), Bytes: u.Bytes, Objects: u.Objects,
			PublicRead: st.PublicRead, MaxSize: st.MaxSize, MaxObjectSize: st.MaxObjectSize, VideoProcess: st.VideoProcess, Managed: st.Managed})
	}
	return out, nil
}

func (h *Handler) endpoint(r *http.Request) string {
	if h.Cfg.PublicURL != "" {
		return strings.TrimSuffix(h.Cfg.PublicURL, "/")
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.bucketViews()
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	var total, objects int64
	for _, b := range buckets {
		total += b.Bytes
		objects += b.Objects
	}
	out := map[string]any{
		"version":     version.Version,
		"uptime":      int64(time.Since(h.Started).Seconds()),
		"endpoint":    h.endpoint(r),
		"region":      h.Cfg.Region,
		"data_dir":    h.Cfg.DataDir,
		"disk_free":   sysinfo.DiskFree(h.Cfg.DataDir),
		"total":       total,
		"objects":     objects,
		"max_total":   int64(h.Cfg.MaxTotal),
		"buckets":     buckets,
		"keys":        len(h.DB.Keys()),
		"requests":    h.S3.Stats.Requests.Load(),
		"uploads":     h.S3.Stats.Uploads.Load(),
		"downloads":   h.S3.Stats.Downloads.Load(),
		"last_backup": h.DB.LastBackup(),
	}
	if h.Video != nil {
		out["video"] = h.Video()
	} else {
		out["video"] = VideoInfo{}
	}
	if h.ImageStats != nil {
		n, b, hits, misses := h.ImageStats()
		out["image_cache"] = map[string]int64{"entries": int64(n), "bytes": b, "hits": hits, "misses": misses}
	}
	writeJSON(w, 200, out)
}

// ---- buckets --------------------------------------------------------------

func (h *Handler) listBuckets(w http.ResponseWriter, r *http.Request) {
	b, err := h.bucketViews()
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

type bucketInput struct {
	Name          string `json:"name"`
	PublicRead    bool   `json:"public_read"`
	MaxSize       string `json:"max_size"`
	MaxObjectSize string `json:"max_object_size"`
	VideoProcess  bool   `json:"video_process"`
}

func (in bucketInput) settings() (storage.BucketSettings, error) {
	ms, err := config.ParseSize(in.MaxSize)
	if err != nil {
		return storage.BucketSettings{}, fmt.Errorf("quota: %w", err)
	}
	mo, err := config.ParseSize(in.MaxObjectSize)
	if err != nil {
		return storage.BucketSettings{}, fmt.Errorf("max file size: %w", err)
	}
	return storage.BucketSettings{PublicRead: in.PublicRead, MaxSize: int64(ms), MaxObjectSize: int64(mo), VideoProcess: in.VideoProcess}, nil
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request) {
	var in bucketInput
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	st, err := in.settings()
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	if err := h.Store.CreateBucket(in.Name); err != nil {
		switch {
		case errors.Is(err, storage.ErrInvalidBucketName):
			apiError(w, 400, "invalid name: use 3-63 lowercase letters, digits, dots or hyphens")
		case errors.Is(err, storage.ErrBucketExists):
			apiError(w, 409, "a bucket with this name already exists")
		default:
			apiError(w, 500, err.Error())
		}
		return
	}
	h.Quota.Track(in.Name)
	if err := h.Store.SetBucketSettings(in.Name, st); err != nil {
		apiError(w, 500, err.Error())
		return
	}
	h.Log.Info("bucket created from admin panel", "bucket", in.Name)
	writeJSON(w, 201, map[string]bool{"ok": true})
}

func (h *Handler) updateBucket(w http.ResponseWriter, r *http.Request, name string) {
	if h.Quota.Settings(name).Managed {
		apiError(w, 409, "this bucket's settings come from the config file; edit tijori.toml instead")
		return
	}
	var in bucketInput
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	st, err := in.settings()
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	if err := h.Store.SetBucketSettings(name, st); err != nil {
		apiError(w, 404, "bucket not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) deleteBucket(w http.ResponseWriter, r *http.Request, name string) {
	force := r.URL.Query().Get("force") == "1"
	if err := h.Store.DeleteBucket(name, force); err != nil {
		switch {
		case errors.Is(err, storage.ErrBucketNotEmpty):
			apiError(w, 409, "the bucket is not empty")
		case errors.Is(err, storage.ErrNoSuchBucket):
			apiError(w, 404, "bucket not found")
		default:
			apiError(w, 500, err.Error())
		}
		return
	}
	h.Quota.Forget(name)
	h.Log.Info("bucket deleted from admin panel", "bucket", name, "force", force)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- objects --------------------------------------------------------------

type fileView struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Type     string `json:"type"`
}

func toFileView(o storage.ObjectInfo) fileView {
	name := path.Base(strings.TrimSuffix(o.Key, "/"))
	return fileView{Key: o.Key, Name: name, Size: o.Size, Modified: o.LastModified.Format(time.RFC3339), Type: o.ContentType}
}

func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket, prefix, search := q.Get("bucket"), q.Get("prefix"), strings.ToLower(strings.TrimSpace(q.Get("q")))
	if !h.Store.BucketExists(bucket) {
		apiError(w, 404, "bucket not found")
		return
	}
	out := map[string]any{"folders": []string{}, "files": []fileView{}}
	if search == "" {
		res, err := h.Store.ListObjects(bucket, storage.ListOptions{Prefix: prefix, Delimiter: "/", Marker: q.Get("marker"), MaxKeys: 500, FetchMeta: true})
		if err != nil {
			apiError(w, 500, err.Error())
			return
		}
		files := []fileView{}
		for _, o := range res.Objects {
			if o.Key == prefix {
				continue // the folder marker of the folder being shown
			}
			files = append(files, toFileView(o))
		}
		out["files"] = files
		if res.CommonPrefixes != nil {
			out["folders"] = res.CommonPrefixes
		}
		if res.IsTruncated {
			out["next"] = res.NextMarker
		}
		writeJSON(w, 200, out)
		return
	}
	var files []fileView
	marker := ""
	for scanned := 0; scanned < 100_000 && len(files) < 500; {
		res, err := h.Store.ListObjects(bucket, storage.ListOptions{Prefix: prefix, Marker: marker, MaxKeys: 1000})
		if err != nil {
			apiError(w, 500, err.Error())
			return
		}
		for _, o := range res.Objects {
			scanned++
			if strings.Contains(strings.ToLower(path.Base(o.Key)), search) {
				if info, err := h.Store.StatObject(bucket, o.Key); err == nil {
					files = append(files, toFileView(info))
				}
			}
		}
		if !res.IsTruncated {
			break
		}
		marker = res.NextMarker
	}
	if files == nil {
		files = []fileView{}
	}
	out["files"] = files
	writeJSON(w, 200, out)
}

func s3Status(w http.ResponseWriter, err error) {
	e := s3api.ToS3Error(err)
	apiError(w, e.Status, e.Message)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket, key := q.Get("bucket"), q.Get("key")
	if !h.Store.BucketExists(bucket) {
		apiError(w, 404, "bucket not found")
		return
	}
	if r.ContentLength < 0 {
		apiError(w, http.StatusLengthRequired, "Content-Length is required")
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = ""
	}
	res, err := h.S3.Put(bucket, key, r.Body, r.ContentLength, storage.PutOptions{ContentType: ct})
	if err != nil {
		s3Status(w, err)
		return
	}
	writeJSON(w, 200, toFileView(res.Info))
}

// Chunked uploads: the browser sends big files in small parts so that each
// request finishes well within reverse-proxy timeouts (Coolify's Traefik
// cuts requests after 60 seconds by default) and failed parts can be retried.

func (h *Handler) startUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Bucket string `json:"bucket"`
		Key    string `json:"key"`
		Size   int64  `json:"size"`
		Type   string `json:"type"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Size < 0 {
		apiError(w, 400, "bad request")
		return
	}
	if !h.Store.BucketExists(in.Bucket) {
		apiError(w, 404, "bucket not found")
		return
	}
	// Check quota and size limits before any byte is sent.
	var existing int64
	if info, err := h.Store.StatObject(in.Bucket, in.Key); err == nil {
		existing = info.Size
	}
	release, err := h.Quota.Reserve(in.Bucket, in.Size, existing)
	if err != nil {
		s3Status(w, err)
		return
	}
	release()
	ct := in.Type
	if ct == "application/octet-stream" {
		ct = ""
	}
	id, err := h.Store.CreateMultipartUpload(in.Bucket, in.Key, storage.PutOptions{ContentType: ct})
	if err != nil {
		s3Status(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": id})
}

func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, err := strconv.Atoi(q.Get("n"))
	if err != nil || n < 1 || n > 10000 {
		apiError(w, 400, "part number must be 1-10000")
		return
	}
	if r.ContentLength < 0 {
		apiError(w, http.StatusLengthRequired, "Content-Length is required")
		return
	}
	p, err := h.S3.UploadPart(q.Get("bucket"), q.Get("key"), q.Get("id"), n, r.Body, r.ContentLength)
	if err != nil {
		s3Status(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"n": p.Number, "etag": p.ETag, "size": p.Size})
}

func (h *Handler) completeUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Bucket string `json:"bucket"`
		Key    string `json:"key"`
		ID     string `json:"id"`
		Parts  []struct {
			N    int    `json:"n"`
			ETag string `json:"etag"`
		} `json:"parts"`
	}
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	parts := make([]storage.CompletePart, len(in.Parts))
	for i, p := range in.Parts {
		parts[i] = storage.CompletePart{Number: p.N, ETag: p.ETag}
	}
	res, err := h.S3.CompleteUpload(in.Bucket, in.Key, in.ID, parts, 0)
	if err != nil {
		s3Status(w, err)
		return
	}
	writeJSON(w, 200, toFileView(res.Info))
}

func (h *Handler) abortUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.Store.AbortMultipartUpload(q.Get("bucket"), q.Get("key"), q.Get("id")); err != nil {
		s3Status(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) deleteObject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket, key := q.Get("bucket"), q.Get("key")
	if !strings.HasSuffix(key, "/") {
		if _, err := h.S3.Delete(bucket, key); err != nil {
			s3Status(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"deleted": 1})
		return
	}
	// Folder: delete everything under it.
	n := 0
	for {
		res, err := h.Store.ListObjects(bucket, storage.ListOptions{Prefix: key, MaxKeys: 1000})
		if err != nil {
			s3Status(w, err)
			return
		}
		if len(res.Objects) == 0 {
			break
		}
		// Delete deepest first so folder markers go last.
		before := n
		for i := len(res.Objects) - 1; i >= 0; i-- {
			if _, err := h.S3.Delete(bucket, res.Objects[i].Key); err == nil {
				n++
			}
		}
		if !res.IsTruncated || n == before {
			break
		}
	}
	h.Log.Info("folder deleted from admin panel", "bucket", bucket, "prefix", key, "objects", n)
	writeJSON(w, 200, map[string]int{"deleted": n})
}

func (h *Handler) createFolder(w http.ResponseWriter, r *http.Request) {
	var in struct{ Bucket, Key string }
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	if !strings.HasSuffix(in.Key, "/") {
		in.Key += "/"
	}
	if _, err := h.S3.Put(in.Bucket, in.Key, strings.NewReader(""), 0, storage.PutOptions{}); err != nil {
		s3Status(w, err)
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

// safeInline lists types the browser may show inline from the admin origin.
func safeInline(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "text/plain") || ct == "application/pdf"
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, info, err := h.Store.GetObject(q.Get("bucket"), q.Get("key"))
	if err != nil {
		s3Status(w, err)
		return
	}
	defer f.Close()
	name := path.Base(info.Key)
	hd := w.Header()
	hd.Set("Content-Type", info.ContentType)
	hd.Set("X-Content-Type-Options", "nosniff")
	if ct := strings.ToLower(info.ContentType); ct != "application/pdf" {
		hd.Set("Content-Security-Policy", "sandbox")
	}
	disp := "attachment"
	if q.Get("inline") == "1" && safeInline(info.ContentType) {
		disp = "inline"
	}
	hd.Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(name))
	hd.Set("Cache-Control", "private, no-cache")
	hd.Set("ETag", `"`+info.ETag+`"`)
	http.ServeContent(w, r, "", info.LastModified, f)
}

func (h *Handler) thumb(w http.ResponseWriter, r *http.Request) {
	if h.Images == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	h.Images.ServeImage(w, r, q.Get("bucket"), q.Get("key"), false)
}

// signingKey picks a key that may perform method on bucket.
func (h *Handler) signingKey(bucket, method string) (state.AccessKey, bool) {
	keys := h.DB.Keys()
	for _, k := range keys {
		if !k.CanAccess(bucket) {
			continue
		}
		if (method == http.MethodPut && k.CanWrite()) || (method == http.MethodGet && k.CanRead()) {
			return k, true
		}
	}
	return state.AccessKey{}, false
}

func objectURL(base, bucket, key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return base + "/" + bucket + "/" + strings.Join(parts, "/")
}

func (h *Handler) presign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Bucket  string `json:"bucket"`
		Key     string `json:"key"`
		Method  string `json:"method"`
		Expires int    `json:"expires"`
	}
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	if in.Method == "" {
		in.Method = http.MethodGet
	}
	if in.Method != http.MethodGet && in.Method != http.MethodPut {
		apiError(w, 400, "method must be GET or PUT")
		return
	}
	exp := time.Duration(in.Expires) * time.Second
	if exp <= 0 || exp > auth.MaxPresignExpiry {
		apiError(w, 400, "expiry must be between 1 second and 7 days")
		return
	}
	k, ok := h.signingKey(in.Bucket, in.Method)
	if !ok {
		apiError(w, 409, "no access key can sign this link; create a key for bucket "+in.Bucket+" first")
		return
	}
	u, err := auth.Presign(auth.PresignOptions{Method: in.Method, URL: objectURL(h.endpoint(r), in.Bucket, in.Key),
		AccessKey: k.AccessKey, SecretKey: k.SecretKey, Region: h.Cfg.Region, Expires: exp})
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	out := map[string]any{"url": u, "expires_at": time.Now().Add(exp).Format(time.RFC3339), "signed_with": k.AccessKey}
	if h.Quota.Settings(in.Bucket).PublicRead {
		out["public_url"] = objectURL(h.endpoint(r), in.Bucket, in.Key)
	}
	writeJSON(w, 200, out)
}

// ---- keys -----------------------------------------------------------------

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, se ctxSession) {
	out := []map[string]any{}
	for _, k := range h.DB.Keys() {
		out = append(out, map[string]any{"access_key": k.AccessKey, "name": k.Name, "buckets": k.Buckets,
			"permission": k.Permission, "created": k.CreatedAt, "from_env": k.FromEnv})
	}
	writeJSON(w, 200, out)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string   `json:"name"`
		Buckets    []string `json:"buckets"`
		Permission string   `json:"permission"`
	}
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	k, err := auth.NewKey(strings.TrimSpace(in.Name), in.Buckets, in.Permission)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	if err := h.DB.AddKey(k); err != nil {
		apiError(w, 500, err.Error())
		return
	}
	h.Log.Info("access key created from admin panel", "key", k.AccessKey, "name", k.Name)
	writeJSON(w, 201, map[string]any{"access_key": k.AccessKey, "secret_key": k.SecretKey, "buckets": k.Buckets, "permission": k.Permission})
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.DB.RevokeKey(id); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	h.Log.Info("access key revoked from admin panel", "key", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- backup & settings ----------------------------------------------------

func (h *Handler) backupStatus(w http.ResponseWriter, r *http.Request) {
	running := h.BackupRunning != nil && h.BackupRunning()
	writeJSON(w, 200, map[string]any{
		"configured":  h.Cfg.Backup.To != "",
		"destination": h.Cfg.Redacted().Backup.To,
		"schedule":    h.Cfg.Backup.Schedule,
		"keep":        h.Cfg.Backup.Keep,
		"running":     running,
		"last":        h.DB.LastBackup(),
	})
}

func (h *Handler) runBackup(w http.ResponseWriter, r *http.Request) {
	if h.RunBackup == nil || h.Cfg.Backup.To == "" {
		apiError(w, 409, "no backup destination configured; set [backup] to = ... in tijori.toml")
		return
	}
	if err := h.RunBackup(); err != nil {
		apiError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]bool{"started": true})
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	file := h.Cfg.File
	if file == "" {
		file = "(none: defaults and environment variables)"
	}
	writeJSON(w, 200, map[string]any{"file": file, "toml": h.Cfg.Redacted().TOML(), "warnings": h.Cfg.Warnings()})
}
