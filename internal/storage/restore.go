package storage

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

// Restoring from a backup into a running server. Files are written the same
// way as uploads (temporary file, fsync, rename), so readers see either the
// old or the new version, never half a file.

// Exists reports whether an object or folder marker is stored.
func (s *Store) Exists(bucket, key string) bool {
	if ValidateKey(key) != nil || !s.BucketExists(bucket) {
		return false
	}
	if isDirKey(key) {
		_, err := os.Stat(s.metaPath(bucket, key))
		return err == nil
	}
	fi, err := os.Stat(s.dataPath(bucket, key))
	return err == nil && !fi.IsDir()
}

// RestoreData writes an object's content from a backup and gives the file
// the backup's modification time, so RestoreMeta can attach the original
// metadata afterwards. An object left without metadata gets it rebuilt on
// first read.
func (s *Store) RestoreData(bucket, key string, r io.Reader, modTime time.Time) (int64, error) {
	if err := ValidateKey(key); err != nil {
		return 0, err
	}
	if isDirKey(key) {
		return 0, ErrFolderNotEmpty
	}
	if !s.BucketExists(bucket) {
		return 0, ErrNoSuchBucket
	}
	f, err := s.newTemp()
	if err != nil {
		return 0, err
	}
	tmp := f.Name()
	n, err := io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return n, err
	}
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()
	dst := s.dataPath(bucket, key)
	if fi, err := os.Lstat(dst); err == nil && fi.IsDir() {
		os.Remove(tmp)
		return n, ErrPathConflict
	}
	if err := renameInto(tmp, dst); err != nil {
		os.Remove(tmp)
		return n, err
	}
	_ = os.Chtimes(dst, modTime, modTime)
	// The metadata of the replaced version no longer applies; the backup's
	// own metadata follows in RestoreMeta.
	_ = os.Remove(s.metaPath(bucket, key))
	return n, nil
}

// RestoreMeta attaches metadata from a backup. For files it is used only
// when it matches the file on disk; otherwise Tijori rebuilds it on read.
// For folder keys ("photos/") it creates the folder marker.
func (s *Store) RestoreMeta(bucket, key string, m ObjectMeta) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if !s.BucketExists(bucket) {
		return ErrNoSuchBucket
	}
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()
	if isDirKey(key) {
		dir := s.dataPath(bucket, key)
		if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
			return ErrPathConflict
		}
		if err := mkdirParents(dir); err != nil {
			return err
		}
		m.Size = 0
	} else {
		fi, err := os.Stat(s.dataPath(bucket, key))
		if err != nil || fi.IsDir() || staleMeta(m, fi) {
			return nil
		}
	}
	b, _ := json.Marshal(m)
	return s.writeFileAtomic(s.metaPath(bucket, key), b)
}

// RestoreBucketMeta stores a bucket's creation time and settings from a
// backup.
func (s *Store) RestoreBucketMeta(bucket string, raw []byte) error {
	var m bucketMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if !s.BucketExists(bucket) {
		return ErrNoSuchBucket
	}
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return s.writeBucketMeta(bucket, m)
}
