package storage

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// FolderStats describes everything under a folder (a key prefix), including
// its subfolders.
type FolderStats struct {
	Files     int64     `json:"files"`
	Folders   int64     `json:"folders"`
	Bytes     int64     `json:"bytes"`
	Modified  time.Time `json:"modified"`
	Truncated bool      `json:"truncated"` // stopped counting at the limit
}

// FolderStats counts the files and bytes under prefix ("" for the whole
// bucket). It reads file sizes from the disk and stops after limit entries,
// so a huge folder cannot keep the disk busy; Truncated then says the
// numbers are a lower bound.
func (s *Store) FolderStats(bucket, prefix string, limit int) (FolderStats, error) {
	var st FolderStats
	if !s.BucketExists(bucket) {
		return st, ErrNoSuchBucket
	}
	dir := s.bucketDir(bucket)
	if prefix != "" {
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if err := ValidateKey(prefix); err != nil {
			return st, err
		}
		dir = s.dataPath(bucket, prefix)
	}
	seen := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil // unreadable entries and the folder itself are skipped
		}
		if seen++; seen > limit {
			st.Truncated = true
			return errStopWalk
		}
		if d.IsDir() {
			st.Folders++
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		st.Files++
		st.Bytes += fi.Size()
		if fi.ModTime().After(st.Modified) {
			st.Modified = fi.ModTime().UTC()
		}
		return nil
	})
	if errors.Is(err, errStopWalk) {
		err = nil
	}
	return st, err
}
