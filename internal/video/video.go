// Package video turns uploaded videos into a thumbnail and smaller
// versions using ffmpeg, and reports results to a signed webhook. Jobs are
// stored on disk so a restart does not lose them. Without ffmpeg the
// feature switches itself off.
package video

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/storage"
)

// Job states.
const (
	Queued  = "queued"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

// Output is one generated file.
type Output struct {
	Preset string `json:"preset"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	Type   string `json:"content_type"`
}

// Job is a unit of work, persisted as JSON.
type Job struct {
	ID       string    `json:"id"`
	Bucket   string    `json:"bucket"`
	Key      string    `json:"key"`
	ETag     string    `json:"etag"`
	Status   string    `json:"status"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error,omitempty"`
	Outputs  []Output  `json:"outputs,omitempty"`
	Attempts int       `json:"attempts"`
}

// PutFunc stores a generated file with quota checks and usage accounting.
type PutFunc func(bucket, key string, body io.Reader, size int64, opt storage.PutOptions) (storage.PutResult, error)

// Manager owns the queue and workers.
type Manager struct {
	Cfg      *config.Config
	Store    *storage.Store
	Enabled  func(bucket string) bool // bucket has video_process on
	Put      PutFunc
	Log      *slog.Logger
	Client   *http.Client
	ffmpeg   string
	version  string
	dir      string
	queue    chan string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	pending  int
	done     atomic.Int64
	failed   atomic.Int64
	startErr error
}

// FindFFmpeg returns the ffmpeg path from config or PATH, and its version.
func FindFFmpeg(configured string) (string, string, error) {
	p := configured
	if p == "" {
		var err error
		if p, err = exec.LookPath("ffmpeg"); err != nil {
			return "", "", errors.New("ffmpeg not found on PATH")
		}
	}
	out, err := exec.Command(p, "-hide_banner", "-version").Output()
	if err != nil {
		return "", "", fmt.Errorf("ffmpeg at %s does not run: %w", p, err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return p, strings.TrimSpace(line), nil
}

// Init detects ffmpeg and loads persisted jobs. It never fails the server:
// without ffmpeg, Available() is false.
func (m *Manager) Init() {
	m.dir = filepath.Join(m.Cfg.DataDir, ".jobs", "video")
	m.queue = make(chan string, 10000)
	if m.Client == nil {
		m.Client = &http.Client{Timeout: 30 * time.Second}
	}
	m.ffmpeg, m.version, m.startErr = FindFFmpeg(m.Cfg.Video.FFmpeg)
	if m.startErr != nil {
		return
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		m.startErr = err
	}
}

// Available reports whether video processing can run.
func (m *Manager) Available() bool { return m.startErr == nil && m.ffmpeg != "" }

// Info describes the processor for the dashboard.
func (m *Manager) Info() (available bool, ffmpeg, version string, queued int, done, failed int64) {
	m.mu.Lock()
	q := m.pending
	m.mu.Unlock()
	return m.Available(), m.ffmpeg, m.version, q, m.done.Load(), m.failed.Load()
}

// Start launches workers and re-queues unfinished jobs from disk.
func (m *Manager) Start() {
	if !m.Available() {
		m.Log.Info("video processing off", "reason", m.startErr)
		return
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	for i := 0; i < m.Cfg.Video.Workers; i++ {
		m.wg.Add(1)
		go m.worker()
	}
	jobs, _ := m.Jobs()
	for _, j := range jobs {
		switch j.Status {
		case Queued, Running:
			m.enqueue(j.ID)
		case Done, Failed:
			if time.Since(j.Updated) > 7*24*time.Hour {
				os.Remove(m.jobPath(j.ID))
			}
		}
	}
	if m.Cfg.Video.Webhook != "" && m.Cfg.Video.WebhookSecret == "" {
		m.Log.Warn("video.webhook is set but video.webhook_secret is empty; webhooks are sent unsigned")
	}
	m.Log.Info("video processing on", "ffmpeg", m.ffmpeg, "workers", m.Cfg.Video.Workers)
}

// Stop cancels running ffmpeg processes and waits for workers. Jobs that
// were running stay "running" on disk and restart next time.
func (m *Manager) Stop() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) jobPath(id string) string { return filepath.Join(m.dir, id+".json") }

func (m *Manager) save(j *Job) error {
	j.Updated = time.Now().UTC()
	b, _ := json.MarshalIndent(j, "", "  ")
	tmp := m.jobPath(j.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.jobPath(j.ID))
}

func (m *Manager) load(id string) (*Job, error) {
	b, err := os.ReadFile(m.jobPath(id))
	if err != nil {
		return nil, err
	}
	var j Job
	return &j, json.Unmarshal(b, &j)
}

// Jobs lists persisted jobs, newest first.
func (m *Manager) Jobs() ([]Job, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	var out []Job
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if j, err := m.load(id); err == nil {
				out = append(out, *j)
			}
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Created.After(out[k].Created) })
	return out, nil
}

func (m *Manager) enqueue(id string) {
	m.mu.Lock()
	m.pending++
	m.mu.Unlock()
	select {
	case m.queue <- id:
	default:
		m.Log.Error("video queue full; job stays on disk and runs after restart", "job", id)
	}
}

var videoExt = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true, ".3gp": true, ".mpeg": true, ".mpg": true}

// IsVideo decides whether an uploaded object should be processed.
func IsVideo(key, contentType string) bool {
	if strings.HasSuffix(key, "/") {
		return false
	}
	ext := strings.ToLower(path.Ext(key))
	for _, p := range []string{"_480p", "_720p", "_1080p"} {
		if strings.HasSuffix(strings.TrimSuffix(key, path.Ext(key)), p) {
			return false // our own output
		}
	}
	return strings.HasPrefix(contentType, "video/") || videoExt[ext]
}

// ObjectCreated implements s3api.Events.
func (m *Manager) ObjectCreated(bucket, key string, info storage.ObjectInfo) {
	if !m.Available() || m.ctx == nil || !IsVideo(key, info.ContentType) || !m.Enabled(bucket) {
		return
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	j := &Job{ID: time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b), Bucket: bucket, Key: key,
		ETag: info.ETag, Status: Queued, Created: time.Now().UTC()}
	if err := m.save(j); err != nil {
		m.Log.Error("cannot save video job", "err", err)
		return
	}
	m.Log.Info("video queued", "bucket", bucket, "key", key, "job", j.ID)
	m.enqueue(j.ID)
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case id := <-m.queue:
			m.mu.Lock()
			m.pending--
			m.mu.Unlock()
			m.run(id)
		}
	}
}

func (m *Manager) run(id string) {
	j, err := m.load(id)
	if err != nil || j.Status == Done || j.Status == Failed {
		return
	}
	j.Status = Running
	j.Attempts++
	_ = m.save(j)
	t0 := time.Now()
	outputs, err := m.process(j)
	if m.ctx.Err() != nil {
		return // shutting down; job resumes on restart
	}
	j.Outputs = outputs
	if err != nil {
		j.Status, j.Error = Failed, err.Error()
		m.failed.Add(1)
		m.Log.Error("video processing failed", "bucket", j.Bucket, "key", j.Key, "err", err)
	} else {
		j.Status = Done
		m.done.Add(1)
		m.Log.Info("video processed", "bucket", j.Bucket, "key", j.Key, "outputs", len(outputs), "took", time.Since(t0).Round(time.Millisecond))
	}
	_ = m.save(j)
	m.notify(j, time.Since(t0))
}

// preset describes one ffmpeg output.
type preset struct {
	suffix, ext, contentType string
	args                     func(in, out string) []string
}

func scaleArgs(height int) func(in, out string) []string {
	return func(in, out string) []string {
		return []string{"-i", in, "-vf", fmt.Sprintf("scale=-2:'min(%d,ih)'", height),
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-f", "mp4", out}
	}
}

var presets = map[string]preset{
	"thumbnail": {suffix: "", ext: ".jpg", contentType: "image/jpeg", args: func(in, out string) []string {
		return []string{"-ss", "1", "-i", in, "-frames:v", "1", "-vf", "scale='min(1280,iw)':-2", "-q:v", "3", "-f", "image2", out}
	}},
	"480p":  {suffix: "_480p", ext: ".mp4", contentType: "video/mp4", args: scaleArgs(480)},
	"720p":  {suffix: "_720p", ext: ".mp4", contentType: "video/mp4", args: scaleArgs(720)},
	"1080p": {suffix: "_1080p", ext: ".mp4", contentType: "video/mp4", args: scaleArgs(1080)},
}

// PresetNames lists supported presets.
func PresetNames() []string { return []string{"thumbnail", "480p", "720p", "1080p"} }

// inputGuard limits what ffmpeg may open for an uploaded file: plain local
// files and real video containers only. Formats that reference other files
// (HLS playlists, concat lists, image sequences) could otherwise make ffmpeg
// read objects from other buckets.
var inputGuard = []string{
	"-protocol_whitelist", "file",
	"-format_whitelist", "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,avi,mpeg,mpegts,flv,asf,ogg",
}

func (m *Manager) ffmpegRun(args []string) error {
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Hour)
	defer cancel()
	var guarded []string
	for _, a := range args {
		if a == "-i" {
			guarded = append(guarded, inputGuard...)
		}
		guarded = append(guarded, a)
	}
	full := append([]string{"-y", "-hide_banner", "-loglevel", "error", "-nostdin"}, guarded...)
	cmd := exec.CommandContext(ctx, m.ffmpeg, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func (m *Manager) process(j *Job) ([]Output, error) {
	in, err := m.Store.DataPath(j.Bucket, j.Key)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(in); err != nil {
		return nil, errors.New("the video was deleted before it could be processed")
	}
	base := strings.TrimSuffix(j.Key, path.Ext(j.Key))
	names := m.Cfg.Video.Presets
	if len(names) == 0 {
		names = []string{"thumbnail", "720p"}
	}
	var outputs []Output
	for _, name := range names {
		p, ok := presets[name]
		if !ok {
			return outputs, fmt.Errorf("unknown video preset %q (use %s)", name, strings.Join(PresetNames(), ", "))
		}
		tmp, err := m.Store.TempFile()
		if err != nil {
			return outputs, err
		}
		tmpPath := tmp.Name() + p.ext
		tmp.Close()
		os.Remove(tmp.Name())
		err = m.ffmpegRun(p.args(in, tmpPath))
		if err != nil && name == "thumbnail" {
			err = m.ffmpegRun(presets["thumbnail"].args(in, tmpPath)[2:]) // clip shorter than 1s: use first frame
		}
		if err != nil {
			os.Remove(tmpPath)
			return outputs, fmt.Errorf("%s: %w", name, err)
		}
		out, err := m.store(j.Bucket, base+p.suffix+p.ext, tmpPath, p.contentType)
		os.Remove(tmpPath)
		if err != nil {
			return outputs, fmt.Errorf("%s: %w", name, err)
		}
		out.Preset = name
		outputs = append(outputs, out)
	}
	return outputs, nil
}

func (m *Manager) store(bucket, key, file, contentType string) (Output, error) {
	f, err := os.Open(file)
	if err != nil {
		return Output{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Output{}, err
	}
	res, err := m.Put(bucket, key, f, fi.Size(), storage.PutOptions{ContentType: contentType, UserMeta: map[string]string{"tijori-generated": "video"}})
	if err != nil {
		return Output{}, err
	}
	return Output{Key: key, Size: res.Info.Size, Type: contentType}, nil
}

// Sign returns the X-Tijori-Signature value for a webhook body.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) notify(j *Job, took time.Duration) {
	url := m.Cfg.Video.Webhook
	if url == "" {
		return
	}
	event := "video.processed"
	if j.Status == Failed {
		event = "video.failed"
	}
	body, _ := json.Marshal(map[string]any{
		"event": event, "job_id": j.ID, "bucket": j.Bucket, "key": j.Key, "etag": j.ETag,
		"outputs": j.Outputs, "error": j.Error, "duration_ms": took.Milliseconds(), "time": time.Now().UTC(),
	})
	for attempt, wait := 1, time.Second; attempt <= 3; attempt, wait = attempt+1, wait*5 {
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Tijori-Webhook/1")
		req.Header.Set("X-Tijori-Event", event)
		if s := m.Cfg.Video.WebhookSecret; s != "" {
			req.Header.Set("X-Tijori-Signature", Sign([]byte(s), body))
		}
		resp, err := m.Client.Do(req)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		m.Log.Warn("webhook failed", "attempt", attempt, "err", err)
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
