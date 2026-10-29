package admin

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/getsettalk/Tijori/internal/backup"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/sysinfo"
	"github.com/getsettalk/Tijori/internal/version"
)

// Full backups through the admin panel: download everything as one .tar.gz
// file (the same format as `tijori export`), and restore such a file into
// the running server. Both ask for the password again, because the file
// holds every access key's secret.

const (
	exportLinkTTL  = 2 * time.Minute
	maxRestorePart = 32 << 20
	// A restore upload nobody continues for this long may be replaced.
	restoreIdle = 15 * time.Minute
)

type exportLink struct {
	session string
	expires time.Time
}

type restoreJob struct {
	ID       string              `json:"id"`
	User     string              `json:"user"`
	State    string              `json:"state"` // uploading, checking, restoring, done, failed
	Size     int64               `json:"size"`
	Received int64               `json:"received"`
	Total    int64               `json:"total"` // file bytes to restore, known after the check
	Done     int64               `json:"done"`
	Replace  bool                `json:"replace"`
	Info     *backup.ArchiveInfo `json:"info,omitempty"`
	Result   *backup.LiveResult  `json:"result,omitempty"`
	Error    string              `json:"error,omitempty"`
	Started  time.Time           `json:"started"`
	Updated  time.Time           `json:"updated"`
	path     string
	write    sync.Mutex // one part at a time
}

type backupFiles struct {
	mu    sync.Mutex
	links map[string]exportLink
	job   *restoreJob
}

// confirmPassword checks the logged-in user's password again. Failures
// count towards the login limit, so it cannot be used to guess passwords.
func (h *Handler) confirmPassword(w http.ResponseWriter, r *http.Request, se ctxSession, password string) bool {
	ip := clientIP(r)
	if blocked, wait := h.limiter.blocked(ip, se.user); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		apiError(w, http.StatusTooManyRequests, fmt.Sprintf("too many wrong passwords; try again in %d minutes", int(wait.Minutes())+1))
		return false
	}
	u, ok := h.DB.User(se.user)
	if !ok || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		h.limiter.fail(ip, se.user)
		h.Log.Warn("admin password check failed", "user", se.user, "remote", ip)
		apiError(w, http.StatusForbidden, "wrong password")
		return false
	}
	return true
}

// ---- download ---------------------------------------------------------------

// exportLink returns a one-time link for the download. The browser then
// opens the link itself, so the file streams to disk however big it is.
func (h *Handler) exportLink(w http.ResponseWriter, r *http.Request, se ctxSession) {
	var in struct{ Password string }
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	if !h.confirmPassword(w, r, se, in.Password) {
		return
	}
	token := randomHex(32)
	now := time.Now()
	h.files.mu.Lock()
	if h.files.links == nil {
		h.files.links = map[string]exportLink{}
	}
	for k, l := range h.files.links {
		if now.After(l.expires) {
			delete(h.files.links, k)
		}
	}
	h.files.links[token] = exportLink{session: se.id, expires: now.Add(exportLinkTTL)}
	h.files.mu.Unlock()
	writeJSON(w, 200, map[string]string{"url": h.base() + "/api/export?token=" + token})
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request, se ctxSession) {
	token := r.URL.Query().Get("token")
	h.files.mu.Lock()
	l, ok := h.files.links[token]
	delete(h.files.links, token)
	h.files.mu.Unlock()
	if !ok || l.session != se.id || time.Now().After(l.expires) || se.role != "admin" {
		apiError(w, http.StatusForbidden, "this download link has expired; click Download again")
		return
	}
	_ = h.DB.Flush()
	name := "tijori-backup-" + time.Now().UTC().Format("2006-01-02-1504") + ".tar.gz"
	hd := w.Header()
	hd.Set("Content-Type", "application/gzip")
	hd.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hd.Set("Cache-Control", "no-store")
	ip := clientIP(r)
	h.Log.Info("full backup download started", "user", se.user, "remote", ip)
	info, err := backup.Export(w, h.Cfg.DataDir, h.Cfg.File, version.Version)
	if err != nil {
		// The file then ends early; restoring it fails with a clear message.
		h.Log.Error("full backup download failed", "user", se.user, "remote", ip, "err", err)
		return
	}
	h.Log.Info("full backup downloaded", "user", se.user, "remote", ip, "files", info.Files, "bytes", info.Bytes)
}

// ---- restore ----------------------------------------------------------------

// The backup file is uploaded in parts (so proxies with time limits don't
// cut it), stored in .tmp, checked completely and only then restored.

func (h *Handler) restoreStart(w http.ResponseWriter, r *http.Request, se ctxSession) {
	var in struct {
		Size     int64
		Password string
	}
	if err := decodeJSON(r, &in); err != nil || in.Size <= 0 {
		apiError(w, 400, "send the file size and your password")
		return
	}
	if !h.confirmPassword(w, r, se, in.Password) {
		return
	}
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	if j := h.files.job; j != nil {
		switch {
		case j.State == "checking" || j.State == "restoring":
			apiError(w, http.StatusConflict, "a restore is already running")
			return
		case j.State == "uploading" && j.User != se.user && time.Since(j.Updated) < restoreIdle:
			apiError(w, http.StatusConflict, j.User+" is uploading a backup file right now")
			return
		case j.State == "uploading":
			os.Remove(j.path)
		}
	}
	if free := sysinfo.DiskFree(h.Cfg.DataDir); free >= 0 && in.Size+(64<<20) > free {
		apiError(w, http.StatusInsufficientStorage, fmt.Sprintf("not enough free disk space for the backup file: it needs %s, %s is free", config.FormatBytes(in.Size), config.FormatBytes(free)))
		return
	}
	f, err := os.CreateTemp(filepath.Join(h.Cfg.DataDir, ".tmp"), "restore-*.tar.gz")
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	f.Close()
	now := time.Now().UTC()
	j := &restoreJob{ID: randomHex(16), User: se.user, State: "uploading", Size: in.Size, Started: now, Updated: now, path: f.Name()}
	h.files.job = j
	h.Log.Info("restore upload started", "user", se.user, "remote", clientIP(r), "size", in.Size)
	writeJSON(w, 201, map[string]string{"id": j.ID})
}

// uploadingJob returns the job this user is uploading with this id.
func (h *Handler) uploadingJob(id, user string) *restoreJob {
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	if j := h.files.job; j != nil && j.ID == id && j.User == user && j.State == "uploading" {
		return j
	}
	return nil
}

// restorePart writes one part at offset. A retried part simply overwrites
// what an earlier, failed attempt left.
func (h *Handler) restorePart(w http.ResponseWriter, r *http.Request, se ctxSession) {
	q := r.URL.Query()
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		apiError(w, 400, "bad offset")
		return
	}
	if r.ContentLength < 0 {
		apiError(w, http.StatusLengthRequired, "Content-Length is required")
		return
	}
	if r.ContentLength > maxRestorePart {
		apiError(w, http.StatusRequestEntityTooLarge, "part too large")
		return
	}
	j := h.uploadingJob(q.Get("id"), se.user)
	if j == nil {
		apiError(w, http.StatusNotFound, "this restore upload is no longer active; start again")
		return
	}
	j.write.Lock()
	defer j.write.Unlock()
	h.files.mu.Lock()
	received, size := j.Received, j.Size
	h.files.mu.Unlock()
	if offset > received || offset+r.ContentLength > size {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "part does not fit the file", "received": received})
		return
	}
	f, err := os.OpenFile(j.path, os.O_WRONLY, 0)
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	defer f.Close()
	n := int64(0)
	if err = f.Truncate(offset); err == nil {
		if _, err = f.Seek(offset, io.SeekStart); err == nil {
			n, err = io.Copy(f, io.LimitReader(r.Body, r.ContentLength))
		}
	}
	if err == nil && n != r.ContentLength {
		err = io.ErrUnexpectedEOF
	}
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	j.Updated = time.Now().UTC()
	if err != nil {
		_ = f.Truncate(offset)
		j.Received = offset
		apiError(w, http.StatusRequestTimeout, "part not received completely; send it again")
		return
	}
	j.Received = offset + n
	writeJSON(w, 200, map[string]int64{"received": j.Received})
}

func (h *Handler) restoreFinish(w http.ResponseWriter, r *http.Request, se ctxSession) {
	var in struct {
		ID      string
		Replace bool
	}
	if err := decodeJSON(r, &in); err != nil {
		apiError(w, 400, "bad request")
		return
	}
	h.files.mu.Lock()
	j := h.files.job
	if j == nil || j.ID != in.ID || j.User != se.user || j.State != "uploading" || j.Received != j.Size {
		h.files.mu.Unlock()
		apiError(w, http.StatusConflict, "the backup file has not been uploaded completely")
		return
	}
	j.State, j.Replace, j.Updated = "checking", in.Replace, time.Now().UTC()
	h.files.mu.Unlock()
	h.Log.Info("restore started", "user", se.user, "remote", clientIP(r), "replace", in.Replace)
	go h.runRestore(j)
	h.restoreStatus(w, r)
}

func (h *Handler) runRestore(j *restoreJob) {
	defer os.Remove(j.path)
	set := func(fn func()) {
		h.files.mu.Lock()
		fn()
		j.Updated = time.Now().UTC()
		h.files.mu.Unlock()
	}
	failed := func(err error) {
		set(func() { j.State, j.Error = "failed", err.Error() })
		h.Log.Warn("restore failed", "user", j.User, "err", err)
	}
	f, err := os.Open(j.path)
	if err != nil {
		failed(err)
		return
	}
	defer f.Close()
	info, err := backup.CheckArchive(f)
	if err != nil {
		failed(err)
		return
	}
	if free := sysinfo.DiskFree(h.Cfg.DataDir); free >= 0 && info.Bytes+(64<<20) > free {
		failed(fmt.Errorf("not enough free disk space: the backup holds %s, %s is free", config.FormatBytes(info.Bytes), config.FormatBytes(free)))
		return
	}
	set(func() { j.Info, j.Total, j.State = &info, info.Bytes, "restoring" })
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		failed(err)
		return
	}
	res, err := backup.RestoreLive(f, h.Store, h.DB, j.Replace, func(done int64) { set(func() { j.Done = done }) })
	if _, qerr := h.Quota.Recalculate(); qerr != nil {
		h.Log.Error("recounting usage after restore", "err", qerr)
	}
	set(func() { j.Result = &res })
	if err != nil {
		failed(fmt.Errorf("stopped after %d files: %w", res.Files, err))
		return
	}
	set(func() { j.State = "done" })
	h.Log.Info("restore finished", "user", j.User, "files", res.Files, "bytes", res.Bytes, "skipped", res.Skipped,
		"buckets_created", len(res.BucketsCreated), "keys_added", res.KeysAdded, "users_added", res.UsersAdded)
}

func (h *Handler) restoreStatus(w http.ResponseWriter, r *http.Request) {
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	if h.files.job == nil {
		writeJSON(w, 200, map[string]string{"state": "none"})
		return
	}
	writeJSON(w, 200, h.files.job)
}

func (h *Handler) restoreCancel(w http.ResponseWriter, r *http.Request, se ctxSession) {
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	j := h.files.job
	if j == nil || j.ID != r.URL.Query().Get("id") || j.User != se.user {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if j.State == "checking" || j.State == "restoring" {
		apiError(w, http.StatusConflict, "the restore is already running")
		return
	}
	if j.State == "uploading" {
		os.Remove(j.path)
	}
	h.files.job = nil
	writeJSON(w, 200, map[string]bool{"ok": true})
}
