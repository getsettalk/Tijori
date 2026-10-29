package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

const (
	manifestName = "manifest.json"
	snapshotDir  = "snapshots"
)

// Manifest records what the destination holds, so later backups only copy
// files whose size or ETag changed.
type Manifest struct {
	Version int                      `json:"version"`
	Updated time.Time                `json:"updated"`
	Buckets []string                 `json:"buckets"`
	Files   map[string]ManifestEntry `json:"files"` // "<bucket>/<key>"
}

// ManifestEntry describes one backed-up object.
type ManifestEntry struct {
	Size int64  `json:"size"`
	ETag string `json:"etag"`
	// Missing counts backups since the object disappeared from Tijori. It
	// is deleted from the destination once Missing reaches keep.
	Missing int `json:"missing,omitempty"`
}

// Options configures a backup run.
type Options struct {
	ConfigFile string // copied into each snapshot when set
	Keep       int    // snapshots to keep (and grace period for deletions)
	Progress   func(format string, args ...any)
}

// Backup layout. Names never contain ".tijori" components so a backup can
// itself be stored in a Tijori bucket.
func dataPath(bucket, key string) string {
	return "data/buckets/" + bucket + "/" + strings.TrimSuffix(key, "/")
}
func metaPath(bucket, key string) string {
	if strings.HasSuffix(key, "/") {
		return "data/folders/" + bucket + "/" + strings.TrimSuffix(key, "/") + ".json"
	}
	return "data/meta/" + bucket + "/" + key + ".json"
}
func bucketMetaPath(bucket string) string { return "data/bucket-settings/" + bucket + ".json" }

func loadManifest(t Target) (*Manifest, error) {
	m := &Manifest{Version: 1, Files: map[string]ManifestEntry{}}
	rc, err := t.Get(manifestName)
	if errors.Is(err, ErrNotFound) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if err := json.NewDecoder(rc).Decode(m); err != nil {
		return nil, fmt.Errorf("backup manifest at %s is damaged: %w", t, err)
	}
	if m.Files == nil {
		m.Files = map[string]ManifestEntry{}
	}
	return m, nil
}

func saveManifest(t Target, m *Manifest) error {
	m.Updated = time.Now().UTC()
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return t.Put(manifestName, bytes.NewReader(b), int64(len(b)))
}

func putFile(t Target, dst, src string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), t.Put(dst, f, fi.Size())
}

// Run copies everything in store, plus a snapshot of the database and
// config, to t.
func Run(store *storage.Store, db *state.DB, t Target, opt Options) (state.BackupStatus, error) {
	st := state.BackupStatus{Started: time.Now().UTC(), Dest: t.String()}
	logf := opt.Progress
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opt.Keep < 1 {
		opt.Keep = 7
	}
	fail := func(err error) (state.BackupStatus, error) {
		st.Finished = time.Now().UTC()
		st.Error = err.Error()
		return st, err
	}
	m, err := loadManifest(t)
	if err != nil {
		return fail(err)
	}
	buckets, err := store.ListBuckets()
	if err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	m.Buckets = m.Buckets[:0]
	sinceSave := 0
	for _, b := range buckets {
		m.Buckets = append(m.Buckets, b.Name)
		if _, err := os.Stat(store.BucketMetaFile(b.Name)); err == nil {
			if _, err := putFile(t, bucketMetaPath(b.Name), store.BucketMetaFile(b.Name)); err != nil {
				return fail(err)
			}
		}
		err := store.WalkObjects(b.Name, func(o storage.ObjectInfo) error {
			info, err := store.StatObject(b.Name, o.Key)
			if err != nil {
				return nil // deleted while walking
			}
			id := b.Name + "/" + o.Key
			seen[id] = true
			if e, ok := m.Files[id]; ok && e.Size == info.Size && e.ETag == info.ETag {
				e.Missing = 0
				m.Files[id] = e
				st.Skipped++
				return nil
			}
			if !strings.HasSuffix(o.Key, "/") {
				src, _ := store.DataPath(b.Name, o.Key)
				if _, err := putFile(t, dataPath(b.Name, o.Key), src); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
			}
			if _, err := putFile(t, metaPath(b.Name, o.Key), store.MetaFile(b.Name, o.Key)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s metadata: %w", id, err)
			}
			m.Files[id] = ManifestEntry{Size: info.Size, ETag: info.ETag}
			st.Copied++
			st.Bytes += info.Size
			if sinceSave++; sinceSave >= 500 {
				sinceSave = 0
				logf("copied %d files so far", st.Copied)
				return saveManifest(t, m)
			}
			return nil
		})
		if err != nil {
			_ = saveManifest(t, m)
			return fail(err)
		}
	}

	// Objects deleted from Tijori stay in the backup for `keep` runs.
	for id, e := range m.Files {
		if seen[id] {
			continue
		}
		e.Missing++
		if e.Missing < opt.Keep {
			m.Files[id] = e
			continue
		}
		bucket, key, _ := strings.Cut(id, "/")
		if !strings.HasSuffix(key, "/") {
			_ = t.Delete(dataPath(bucket, key))
		}
		_ = t.Delete(metaPath(bucket, key))
		delete(m.Files, id)
	}
	if err := saveManifest(t, m); err != nil {
		return fail(err)
	}

	// Snapshot keys/users and config, then prune old snapshots.
	snap := snapshotDir + "/" + time.Now().UTC().Format("20060102-150405")
	if _, err := putFile(t, snap+"/"+state.FileName, db.Path()); err != nil {
		return fail(err)
	}
	if opt.ConfigFile != "" {
		if _, err := putFile(t, snap+"/tijori.toml", opt.ConfigFile); err != nil {
			return fail(err)
		}
	}
	if snaps, err := t.ListDirs(snapshotDir); err == nil && len(snaps) > opt.Keep {
		sort.Strings(snaps)
		for _, old := range snaps[:len(snaps)-opt.Keep] {
			_ = t.Delete(snapshotDir + "/" + old + "/" + state.FileName)
			_ = t.Delete(snapshotDir + "/" + old + "/tijori.toml")
		}
	}
	st.Finished = time.Now().UTC()
	st.OK = true
	logf("backup finished: %d copied, %d unchanged", st.Copied, st.Skipped)
	return st, nil
}

// RestoreResult summarises a restore.
type RestoreResult struct {
	Files, Skipped int
	Bytes          int64
	Snapshot       string
	ConfigFile     string // where a restored tijori.toml was written, if any
}

func fetchTo(t Target, src, dst string, perm os.FileMode) (int64, error) {
	rc, err := t.Get(src)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".tijori-restore-*")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, rc)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), dst)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return n, err
}

// Restore copies a backup into dataDir. snapshot may be "" for the newest.
// The server must not be running.
func Restore(t Target, dataDir, snapshot string, progress func(string, ...any)) (RestoreResult, error) {
	var res RestoreResult
	if progress == nil {
		progress = func(string, ...any) {}
	}
	m, err := loadManifest(t)
	if err != nil {
		return res, err
	}
	if len(m.Buckets) == 0 && len(m.Files) == 0 {
		return res, fmt.Errorf("no Tijori backup found at %s", t)
	}
	store, err := storage.Open(dataDir)
	if err != nil {
		return res, err
	}
	for _, b := range m.Buckets {
		if !store.BucketExists(b) {
			if err := store.CreateBucket(b); err != nil {
				return res, fmt.Errorf("bucket %s: %w", b, err)
			}
		}
		_, _ = fetchTo(t, bucketMetaPath(b), store.BucketMetaFile(b), 0o644)
	}
	ids := make([]string, 0, len(m.Files))
	for id, e := range m.Files {
		if e.Missing == 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := m.Files[id]
		bucket, key, _ := strings.Cut(id, "/")
		if storage.ValidateKey(key) != nil {
			continue
		}
		if info, err := store.StatObject(bucket, key); err == nil && info.Size == e.Size && info.ETag == e.ETag {
			res.Skipped++
			continue
		}
		if !strings.HasSuffix(key, "/") {
			dst, _ := store.DataPath(bucket, key)
			n, err := fetchTo(t, dataPath(bucket, key), dst, 0o644)
			if err != nil {
				return res, fmt.Errorf("%s: %w", id, err)
			}
			res.Bytes += n
		} else {
			dir, _ := store.DataPath(bucket, key)
			_ = os.MkdirAll(dir, 0o755)
		}
		if _, err := fetchTo(t, metaPath(bucket, key), store.MetaFile(bucket, key), 0o644); err != nil && !errors.Is(err, ErrNotFound) {
			return res, fmt.Errorf("%s metadata: %w", id, err)
		}
		// Give the file its original time so the metadata is not seen as stale.
		if b, err := os.ReadFile(store.MetaFile(bucket, key)); err == nil && !strings.HasSuffix(key, "/") {
			var meta storage.ObjectMeta
			if json.Unmarshal(b, &meta) == nil && !meta.LastModified.IsZero() {
				dst, _ := store.DataPath(bucket, key)
				_ = os.Chtimes(dst, meta.LastModified, meta.LastModified)
			}
		}
		res.Files++
		if res.Files%500 == 0 {
			progress("restored %d files", res.Files)
		}
	}

	if snapshot == "" {
		snaps, err := t.ListDirs(snapshotDir)
		if err != nil {
			return res, err
		}
		sort.Strings(snaps)
		// Pick the newest snapshot that still has a database file.
		for i := len(snaps) - 1; i >= 0 && snapshot == ""; i-- {
			if rc, err := t.Get(snapshotDir + "/" + snaps[i] + "/" + state.FileName); err == nil {
				rc.Close()
				snapshot = snaps[i]
			}
		}
	}
	if snapshot != "" {
		base := snapshotDir + "/" + path.Base(snapshot)
		if _, err := fetchTo(t, base+"/"+state.FileName, filepath.Join(dataDir, state.FileName), 0o600); err != nil {
			return res, fmt.Errorf("restoring keys from snapshot %s: %w", snapshot, err)
		}
		cfg := filepath.Join(dataDir, "tijori.restored.toml")
		if _, err := fetchTo(t, base+"/tijori.toml", cfg, 0o600); err == nil {
			res.ConfigFile = cfg
		}
		res.Snapshot = snapshot
	}
	return res, nil
}
