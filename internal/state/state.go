// Package state persists Tijori's small database (tijori.db.json): access
// keys, admin users, usage counters and backup status. It is one JSON file,
// written atomically with 0600 permissions.
package state

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

const FileName = "tijori.db.json"

// Permission levels for access keys.
const (
	PermRead      = "read"
	PermWrite     = "write"
	PermReadWrite = "readwrite"
)

// AccessKey is an S3 credential. The secret is stored as-is because SigV4
// verification needs the raw secret to compute the HMAC.
type AccessKey struct {
	AccessKey  string    `json:"access_key"`
	SecretKey  string    `json:"secret_key"`
	Name       string    `json:"name,omitempty"`
	Buckets    []string  `json:"buckets"` // "*" means every bucket
	Permission string    `json:"permission"`
	CreatedAt  time.Time `json:"created_at"`
	// FromEnv marks the root key supplied by environment variables; it is
	// never written to disk.
	FromEnv bool `json:"-"`
}

// AllBuckets reports whether the key may access every bucket.
func (k *AccessKey) AllBuckets() bool { return slices.Contains(k.Buckets, "*") }

// CanAccess reports whether the key may touch bucket at all.
func (k *AccessKey) CanAccess(bucket string) bool {
	return k.AllBuckets() || slices.Contains(k.Buckets, bucket)
}

func (k *AccessKey) CanRead() bool { return k.Permission == PermRead || k.Permission == PermReadWrite }
func (k *AccessKey) CanWrite() bool {
	return k.Permission == PermWrite || k.Permission == PermReadWrite
}

// AdminUser is a person who can log in to the admin panel.
type AdminUser struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Role         string    `json:"role"` // "admin" or "readonly"
	CreatedAt    time.Time `json:"created_at"`
}

// Usage is the stored size and object count of one bucket.
type Usage struct {
	Bytes   int64 `json:"bytes"`
	Objects int64 `json:"objects"`
}

// BackupStatus records the outcome of the most recent backup.
type BackupStatus struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Dest     string    `json:"dest"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Copied   int       `json:"copied"`
	Skipped  int       `json:"skipped"`
	Bytes    int64     `json:"bytes"`
}

type data struct {
	Version       int              `json:"version"`
	Keys          []AccessKey      `json:"keys"`
	Users         []AdminUser      `json:"users"`
	Usage         map[string]Usage `json:"usage"`
	LastBackup    *BackupStatus    `json:"last_backup,omitempty"`
	SessionSecret []byte           `json:"session_secret"`
}

// DB is the in-memory, mutex-protected view of tijori.db.json.
//
// The CLI and a running server may both change the file, so every write
// first merges keys, users and backup status from disk, and reads re-check
// the file at most every two seconds. Usage counters belong to the server
// process and are never taken from disk after startup.
type DB struct {
	mu        sync.Mutex
	path      string
	d         data
	envKey    *AccessKey
	dirty     bool
	modTime   time.Time
	lastCheck time.Time
}

// Open loads (or creates) the database in dataDir.
func Open(dataDir string) (*DB, error) {
	db := &DB{path: filepath.Join(dataDir, FileName)}
	b, err := os.ReadFile(db.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		db.d = data{Version: 1}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &db.d); err != nil {
			return nil, fmt.Errorf("%s is corrupt: %w", db.path, err)
		}
	}
	if db.d.Usage == nil {
		db.d.Usage = map[string]Usage{}
	}
	db.modTime = fileModTime(db.path)
	if len(db.d.SessionSecret) < 32 {
		db.d.SessionSecret = make([]byte, 32)
		_, _ = rand.Read(db.d.SessionSecret)
		if err := db.save(); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func fileModTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// syncLocked merges changes another process wrote to the file.
func (db *DB) syncLocked() {
	mt := fileModTime(db.path)
	if mt.IsZero() || mt.Equal(db.modTime) {
		return
	}
	b, err := os.ReadFile(db.path)
	if err != nil {
		return
	}
	var disk data
	if json.Unmarshal(b, &disk) != nil {
		return
	}
	db.d.Keys, db.d.Users, db.d.LastBackup = disk.Keys, disk.Users, disk.LastBackup
	if len(disk.SessionSecret) >= 32 {
		db.d.SessionSecret = disk.SessionSecret
	}
	db.modTime = mt
}

// refreshLocked re-checks the file at most every two seconds.
func (db *DB) refreshLocked() {
	if time.Since(db.lastCheck) < 2*time.Second {
		return
	}
	db.lastCheck = time.Now()
	db.syncLocked()
}

// Path is the database file location.
func (db *DB) Path() string { return db.path }

func (db *DB) save() error {
	b, err := json.MarshalIndent(&db.d, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(db.path, b, 0o600); err != nil {
		return err
	}
	db.modTime = fileModTime(db.path)
	return nil
}

// mutate applies fn to fresh data and saves it.
func (db *DB) mutate(fn func() error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.syncLocked()
	if err := fn(); err != nil {
		return err
	}
	return db.save()
}

// WriteFileAtomic writes via a temp file + fsync + rename.
func WriteFileAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SetEnvKey registers the root key from environment variables (not saved).
func (db *DB) SetEnvKey(access, secret string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if access == "" {
		db.envKey = nil
		return
	}
	db.envKey = &AccessKey{AccessKey: access, SecretKey: secret, Name: "root (from environment)",
		Buckets: []string{"*"}, Permission: PermReadWrite, FromEnv: true}
}

// Key looks up an access key by its ID.
func (db *DB) Key(id string) (AccessKey, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	if db.envKey != nil && db.envKey.AccessKey == id {
		return *db.envKey, true
	}
	for _, k := range db.d.Keys {
		if k.AccessKey == id {
			return k, true
		}
	}
	return AccessKey{}, false
}

// Keys lists all keys, environment key first.
func (db *DB) Keys() []AccessKey {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	var out []AccessKey
	if db.envKey != nil {
		out = append(out, *db.envKey)
	}
	return append(out, db.d.Keys...)
}

// AddKey stores a new key.
func (db *DB) AddKey(k AccessKey) error {
	return db.mutate(func() error {
		for _, e := range db.d.Keys {
			if e.AccessKey == k.AccessKey {
				return fmt.Errorf("key %s already exists", k.AccessKey)
			}
		}
		db.d.Keys = append(db.d.Keys, k)
		return nil
	})
}

// UpdateKey changes the buckets, permission and name of an existing key.
func (db *DB) UpdateKey(id string, fn func(*AccessKey)) error {
	return db.mutate(func() error {
		for i := range db.d.Keys {
			if db.d.Keys[i].AccessKey == id {
				fn(&db.d.Keys[i])
				return nil
			}
		}
		return fmt.Errorf("key %s not found", id)
	})
}

// RevokeKey deletes a key.
func (db *DB) RevokeKey(id string) error {
	return db.mutate(func() error {
		for i, k := range db.d.Keys {
			if k.AccessKey == id {
				db.d.Keys = slices.Delete(db.d.Keys, i, i+1)
				return nil
			}
		}
		if db.envKey != nil && db.envKey.AccessKey == id {
			return errors.New("this key comes from TIJORI_ACCESS_KEY; remove the environment variable instead")
		}
		return fmt.Errorf("key %s not found", id)
	})
}

// User returns an admin user by name.
func (db *DB) User(name string) (AdminUser, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	for _, u := range db.d.Users {
		if u.Username == name {
			return u, true
		}
	}
	return AdminUser{}, false
}

// Users lists admin users.
func (db *DB) Users() []AdminUser {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	return slices.Clone(db.d.Users)
}

// PutUser creates or replaces an admin user.
func (db *DB) PutUser(u AdminUser) error {
	return db.mutate(func() error {
		for i := range db.d.Users {
			if db.d.Users[i].Username == u.Username {
				db.d.Users[i] = u
				return nil
			}
		}
		db.d.Users = append(db.d.Users, u)
		return nil
	})
}

// DeleteUser removes an admin user.
func (db *DB) DeleteUser(name string) error {
	return db.mutate(func() error {
		for i, u := range db.d.Users {
			if u.Username == name {
				db.d.Users = slices.Delete(db.d.Users, i, i+1)
				return nil
			}
		}
		return fmt.Errorf("user %s not found", name)
	})
}

// SessionSecret is the HMAC key for admin session cookies.
func (db *DB) SessionSecret() []byte {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	return slices.Clone(db.d.SessionSecret)
}

// Usage returns a copy of all usage counters.
func (db *DB) Usage() map[string]Usage {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	out := make(map[string]Usage, len(db.d.Usage))
	for k, v := range db.d.Usage {
		out[k] = v
	}
	return out
}

// SetUsage replaces all usage counters (after a recalculation).
func (db *DB) SetUsage(u map[string]Usage) error {
	return db.mutate(func() error {
		db.d.Usage = u
		db.dirty = false
		return nil
	})
}

// SaveUsage stores counters without forcing a save on every upload; callers
// flush periodically with Flush.
func (db *DB) SaveUsage(u map[string]Usage) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.d.Usage = u
	db.dirty = true
}

// Flush writes pending usage changes.
func (db *DB) Flush() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.dirty {
		return nil
	}
	db.dirty = false
	db.syncLocked()
	return db.save()
}

// LastBackup returns the last backup status, if any.
func (db *DB) LastBackup() *BackupStatus {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.refreshLocked()
	if db.d.LastBackup == nil {
		return nil
	}
	b := *db.d.LastBackup
	return &b
}

// SetLastBackup records a backup result.
func (db *DB) SetLastBackup(b BackupStatus) error {
	return db.mutate(func() error {
		db.d.LastBackup = &b
		return nil
	})
}

// Reload re-reads the file from disk (used after `tijori import`).
func (db *DB) Reload() error {
	b, err := os.ReadFile(db.path)
	if err != nil {
		return err
	}
	var d data
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if d.Usage == nil {
		d.Usage = map[string]Usage{}
	}
	db.d = d
	db.modTime = fileModTime(db.path)
	return nil
}

// SortedBucketNames is a helper for stable output.
func SortedBucketNames(m map[string]Usage) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
