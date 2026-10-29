// Package quota tracks per-bucket usage and enforces size limits. Uploads
// reserve their declared size before any byte is written, so concurrent
// uploads cannot overshoot a quota together.
package quota

import (
	"fmt"
	"sync"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/s3err"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

// Settings are the effective options of one bucket.
type Settings struct {
	PublicRead    bool
	MaxSize       int64
	MaxObjectSize int64
	VideoProcess  bool
	// Managed is true when the bucket has a [buckets.<name>] section in the
	// config file; the admin panel then shows settings read-only.
	Managed bool
}

// Manager owns usage counters and quota checks.
type Manager struct {
	cfg   *config.Config
	store *storage.Store
	db    *state.DB

	mu            sync.Mutex
	usage         map[string]state.Usage
	reserved      map[string]int64
	reservedTotal int64
}

// New creates a Manager seeded with the counters saved in the database.
func New(cfg *config.Config, store *storage.Store, db *state.DB) *Manager {
	return &Manager{cfg: cfg, store: store, db: db, usage: db.Usage(), reserved: map[string]int64{}}
}

// Settings resolves a bucket's settings: the config file wins, otherwise
// the values saved from the admin panel apply.
func (m *Manager) Settings(bucket string) Settings {
	if b, ok := m.cfg.Bucket(bucket); ok {
		return Settings{PublicRead: b.PublicRead, MaxSize: int64(b.MaxSize), MaxObjectSize: int64(b.MaxObjectSize),
			VideoProcess: b.VideoProcess, Managed: true}
	}
	s := m.store.BucketSettings(bucket)
	return Settings{PublicRead: s.PublicRead, MaxSize: s.MaxSize, MaxObjectSize: s.MaxObjectSize, VideoProcess: s.VideoProcess}
}

// MaxObjectSize is the largest object allowed in bucket (0 = unlimited).
func (m *Manager) MaxObjectSize(bucket string) int64 {
	if s := m.Settings(bucket); s.MaxObjectSize > 0 {
		return s.MaxObjectSize
	}
	return int64(m.cfg.MaxObjectSize)
}

// Recalculate walks every bucket and rebuilds the counters from disk.
func (m *Manager) Recalculate() (map[string]state.Usage, error) {
	buckets, err := m.store.ListBuckets()
	if err != nil {
		return nil, err
	}
	fresh := make(map[string]state.Usage, len(buckets))
	for _, b := range buckets {
		n, c, err := m.store.BucketUsage(b.Name)
		if err != nil {
			return nil, fmt.Errorf("bucket %s: %w", b.Name, err)
		}
		fresh[b.Name] = state.Usage{Bytes: n, Objects: c}
	}
	m.mu.Lock()
	m.usage = fresh
	m.mu.Unlock()
	return fresh, m.db.SetUsage(copyUsage(fresh))
}

func copyUsage(u map[string]state.Usage) map[string]state.Usage {
	out := make(map[string]state.Usage, len(u))
	for k, v := range u {
		out[k] = v
	}
	return out
}

// Usage returns a snapshot of per-bucket usage.
func (m *Manager) Usage() map[string]state.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copyUsage(m.usage)
}

// BucketUsage returns one bucket's usage.
func (m *Manager) BucketUsage(bucket string) state.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage[bucket]
}

// Total returns the bytes used across all buckets.
func (m *Manager) Total() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalLocked()
}

func (m *Manager) totalLocked() int64 {
	var t int64
	for _, u := range m.usage {
		t += u.Bytes
	}
	return t
}

// Reserve checks that an object of size bytes may be written to bucket,
// replacing an existing object of replacing bytes, and holds the space until
// the returned release function is called. On failure it returns an S3
// error (EntityTooLarge or QuotaExceeded) and nothing must be written.
func (m *Manager) Reserve(bucket string, size, replacing int64) (func(), error) {
	if err := m.checkObjectSize(bucket, size); err != nil {
		return nil, err
	}
	s := m.Settings(bucket)
	m.mu.Lock()
	defer m.mu.Unlock()
	growth := size - replacing
	if growth < 0 {
		growth = 0
	}
	if s.MaxSize > 0 {
		used := m.usage[bucket].Bytes + m.reserved[bucket]
		if used+growth > s.MaxSize {
			return nil, s3err.QuotaExceeded.WithMessage(fmt.Sprintf(
				"Bucket %q is full: quota %s, used %s, this upload needs %s more.",
				bucket, config.FormatBytes(s.MaxSize), config.FormatBytes(used), config.FormatBytes(growth)))
		}
	}
	if max := int64(m.cfg.MaxTotal); max > 0 {
		used := m.totalLocked() + m.reservedTotal
		if used+growth > max {
			return nil, s3err.QuotaExceeded.WithMessage(fmt.Sprintf(
				"Server storage limit reached: max_total %s, used %s, this upload needs %s more.",
				config.FormatBytes(max), config.FormatBytes(used), config.FormatBytes(growth)))
		}
	}
	m.reserved[bucket] += growth
	m.reservedTotal += growth
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.reserved[bucket] -= growth
			m.reservedTotal -= growth
			if m.reserved[bucket] == 0 {
				delete(m.reserved, bucket)
			}
			m.mu.Unlock()
		})
	}, nil
}

// CheckPart validates a new multipart part of size bytes. uploadPending is
// what this upload already holds; bucketPending and allPending are the
// parts of every unfinished upload in the bucket and on the server, which
// count against quotas until the uploads complete or are aborted.
func (m *Manager) CheckPart(bucket string, size, uploadPending, bucketPending, allPending int64) error {
	if err := m.checkObjectSize(bucket, uploadPending+size); err != nil {
		return err
	}
	s := m.Settings(bucket)
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.MaxSize > 0 {
		used := m.usage[bucket].Bytes + m.reserved[bucket] + bucketPending
		if used+size > s.MaxSize {
			return s3err.QuotaExceeded.WithMessage(fmt.Sprintf(
				"Bucket %q is full: quota %s, used %s (including unfinished uploads), this part needs %s more.",
				bucket, config.FormatBytes(s.MaxSize), config.FormatBytes(used), config.FormatBytes(size)))
		}
	}
	if max := int64(m.cfg.MaxTotal); max > 0 {
		used := m.totalLocked() + m.reservedTotal + allPending
		if used+size > max {
			return s3err.QuotaExceeded.WithMessage(fmt.Sprintf(
				"Server storage limit reached: max_total %s, used %s (including unfinished uploads).",
				config.FormatBytes(max), config.FormatBytes(used)))
		}
	}
	return nil
}

func (m *Manager) checkObjectSize(bucket string, size int64) error {
	if max := m.MaxObjectSize(bucket); max > 0 && size > max {
		return s3err.EntityTooLarge.WithMessage(fmt.Sprintf(
			"Object is %s but the limit for bucket %q is %s.", config.FormatBytes(size), bucket, config.FormatBytes(max)))
	}
	return nil
}

// Add records a change in stored bytes and object count for bucket.
func (m *Manager) Add(bucket string, bytes, objects int64) {
	m.mu.Lock()
	u := m.usage[bucket]
	u.Bytes += bytes
	u.Objects += objects
	if u.Bytes < 0 {
		u.Bytes = 0
	}
	if u.Objects < 0 {
		u.Objects = 0
	}
	m.usage[bucket] = u
	snapshot := copyUsage(m.usage)
	m.mu.Unlock()
	m.db.SaveUsage(snapshot)
}

// Forget drops counters for a deleted bucket.
func (m *Manager) Forget(bucket string) {
	m.mu.Lock()
	delete(m.usage, bucket)
	snapshot := copyUsage(m.usage)
	m.mu.Unlock()
	m.db.SaveUsage(snapshot)
}

// Track ensures a newly created bucket has a zero counter.
func (m *Manager) Track(bucket string) {
	m.mu.Lock()
	if _, ok := m.usage[bucket]; !ok {
		m.usage[bucket] = state.Usage{}
	}
	m.mu.Unlock()
}
