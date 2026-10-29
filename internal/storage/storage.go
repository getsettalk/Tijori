// Package storage is Tijori's filesystem backend. Objects are plain files
// under <data_dir>/buckets/<bucket>/<key>; metadata lives in a parallel tree
// under <data_dir>/meta. Every write goes to .tmp first, is fsynced and then
// renamed into place, so readers never see half-written files.
package storage

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getsettalk/Tijori/internal/config"
)

var (
	ErrNoSuchBucket      = errors.New("the specified bucket does not exist")
	ErrBucketExists      = errors.New("the requested bucket name is not available")
	ErrBucketNotEmpty    = errors.New("the bucket you tried to delete is not empty")
	ErrInvalidBucketName = errors.New("the specified bucket is not valid")
	ErrNoSuchKey         = errors.New("the specified key does not exist")
	ErrInvalidKey        = errors.New("object key is not allowed (empty parts, '.', '..', backslash or NUL)")
	ErrKeyTooLong        = errors.New("your key is too long")
	ErrPathConflict      = errors.New("object key conflicts with an existing object or folder of the same name")
	ErrBadDigest         = errors.New("the Content-MD5 you specified did not match what we received")
	ErrIncompleteBody    = errors.New("you did not provide the number of bytes specified by the Content-Length HTTP header")
	ErrEntityTooLarge    = errors.New("your proposed upload exceeds the maximum allowed object size")
	ErrFolderNotEmpty    = errors.New("folder objects (keys ending in /) must have an empty body")
)

// ObjectMeta is stored as JSON next to each object.
type ObjectMeta struct {
	ContentType        string            `json:"content_type"`
	ETag               string            `json:"etag"`
	Size               int64             `json:"size"`
	LastModified       time.Time         `json:"last_modified"`
	UserMeta           map[string]string `json:"user_meta,omitempty"`
	CacheControl       string            `json:"cache_control,omitempty"`
	ContentDisposition string            `json:"content_disposition,omitempty"`
	ContentEncoding    string            `json:"content_encoding,omitempty"`
	ContentLanguage    string            `json:"content_language,omitempty"`
	Expires            string            `json:"expires,omitempty"`
	Parts              int               `json:"parts,omitempty"`
}

// ObjectInfo is an object's identity plus metadata.
type ObjectInfo struct {
	Bucket string
	Key    string
	ObjectMeta
}

// BucketInfo describes a bucket.
type BucketInfo struct {
	Name    string
	Created time.Time
}

// BucketSettings are per-bucket options editable from the admin panel. The
// config file's [buckets.<name>] section, when present, takes precedence.
type BucketSettings struct {
	PublicRead    bool  `json:"public_read"`
	MaxSize       int64 `json:"max_size"`
	MaxObjectSize int64 `json:"max_object_size"`
	VideoProcess  bool  `json:"video_process"`
}

type bucketMeta struct {
	Created  time.Time      `json:"created"`
	Settings BucketSettings `json:"settings"`
}

// PutOptions carries headers and limits for a write.
type PutOptions struct {
	ContentType        string
	UserMeta           map[string]string
	CacheControl       string
	ContentDisposition string
	ContentEncoding    string
	ContentLanguage    string
	Expires            string
	ContentMD5         string // base64, optional
	Size               int64  // expected size; -1 if unknown
	MaxSize            int64  // 0 = unlimited
}

// PutResult describes a completed write.
type PutResult struct {
	Info     ObjectInfo
	Replaced bool
	OldSize  int64
}

// Store is the filesystem backend.
type Store struct {
	root  string
	locks [256]sync.RWMutex
	bmu   sync.Mutex // serialises bucket create/delete
}

// Open prepares dataDir's layout and returns a Store.
func Open(dataDir string) (*Store, error) {
	s := &Store{root: dataDir}
	for _, d := range []string{"buckets", "meta", ".tmp", ".multipart", filepath.Join(".cache", "images")} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o755); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", filepath.Join(dataDir, d), err)
		}
	}
	s.cleanTmp(24 * time.Hour)
	return s, nil
}

// Root returns the data directory.
func (s *Store) Root() string { return s.root }

// cleanTmp removes leftovers from interrupted uploads.
func (s *Store) cleanTmp(olderThan time.Duration) {
	dir := filepath.Join(s.root, ".tmp")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > olderThan {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func (s *Store) lock(bucket, key string) *sync.RWMutex {
	h := fnv.New32a()
	h.Write([]byte(bucket))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return &s.locks[h.Sum32()%uint32(len(s.locks))]
}

// ---- Buckets ----------------------------------------------------------------

// ListBuckets returns all buckets sorted by name.
func (s *Store) ListBuckets() ([]BucketInfo, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "buckets"))
	if err != nil {
		return nil, err
	}
	var out []BucketInfo
	for _, e := range entries {
		if !e.IsDir() || !config.ValidBucketName(e.Name()) {
			continue
		}
		bi := BucketInfo{Name: e.Name()}
		if m, err := s.readBucketMeta(e.Name()); err == nil {
			bi.Created = m.Created
		} else if fi, err := e.Info(); err == nil {
			bi.Created = fi.ModTime()
		}
		out = append(out, bi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// BucketExists reports whether a bucket exists.
func (s *Store) BucketExists(bucket string) bool {
	if !config.ValidBucketName(bucket) {
		return false
	}
	fi, err := os.Stat(s.bucketDir(bucket))
	return err == nil && fi.IsDir()
}

// CreateBucket creates an empty bucket.
func (s *Store) CreateBucket(bucket string) error {
	if !config.ValidBucketName(bucket) {
		return ErrInvalidBucketName
	}
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if err := os.Mkdir(s.bucketDir(bucket), 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrBucketExists
		}
		return err
	}
	return s.writeBucketMeta(bucket, bucketMeta{Created: time.Now().UTC()})
}

// DeleteBucket removes an empty bucket. With force, all objects are removed.
func (s *Store) DeleteBucket(bucket string, force bool) error {
	if !s.BucketExists(bucket) {
		return ErrNoSuchBucket
	}
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if !force {
		entries, err := os.ReadDir(s.bucketDir(bucket))
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return ErrBucketNotEmpty
		}
		if err := os.Remove(s.bucketDir(bucket)); err != nil {
			return err
		}
	} else if err := os.RemoveAll(s.bucketDir(bucket)); err != nil {
		return err
	}
	return os.RemoveAll(s.bucketMetaDir(bucket))
}

func (s *Store) readBucketMeta(bucket string) (bucketMeta, error) {
	var m bucketMeta
	b, err := os.ReadFile(filepath.Join(s.bucketMetaDir(bucket), bucketMetaName))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func (s *Store) writeBucketMeta(bucket string, m bucketMeta) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return s.writeFileAtomic(filepath.Join(s.bucketMetaDir(bucket), bucketMetaName), b)
}

// BucketSettings returns the stored settings for a bucket.
func (s *Store) BucketSettings(bucket string) BucketSettings {
	m, _ := s.readBucketMeta(bucket)
	return m.Settings
}

// SetBucketSettings stores settings for a bucket.
func (s *Store) SetBucketSettings(bucket string, st BucketSettings) error {
	if !s.BucketExists(bucket) {
		return ErrNoSuchBucket
	}
	s.bmu.Lock()
	defer s.bmu.Unlock()
	m, err := s.readBucketMeta(bucket)
	if err != nil {
		m.Created = time.Now().UTC()
	}
	m.Settings = st
	return s.writeBucketMeta(bucket, m)
}

// ---- Objects ----------------------------------------------------------------

// newTemp creates a temporary file in .tmp.
func (s *Store) newTemp() (*os.File, error) {
	return os.CreateTemp(filepath.Join(s.root, ".tmp"), "up-*")
}

func (s *Store) writeFileAtomic(dst string, b []byte) error {
	f, err := s.newTemp()
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameInto(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// renameInto renames src to dst, creating dst's parent directories. A
// concurrent delete may prune an empty parent between MkdirAll and Rename,
// so ENOENT is retried a few times.
func renameInto(src, dst string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = mkdirParents(filepath.Dir(dst)); err != nil {
			return err
		}
		if err = os.Rename(src, dst); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
		if _, serr := os.Stat(src); serr != nil {
			break
		}
	}
	if err != nil && isConflict(err) {
		return ErrPathConflict
	}
	return err
}

func mkdirParents(dir string) error {
	err := os.MkdirAll(dir, 0o755)
	if err != nil && isConflict(err) {
		return ErrPathConflict
	}
	return err
}

func isConflict(err error) bool {
	return errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR) ||
		errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTEMPTY)
}

// DetectContentType guesses a MIME type from the key's extension.
func DetectContentType(key string) string {
	if ct := mime.TypeByExtension(strings.ToLower(path.Ext(key))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// PutObject stores an object from r.
func (s *Store) PutObject(bucket, key string, r io.Reader, opt PutOptions) (PutResult, error) {
	if err := ValidateKey(key); err != nil {
		return PutResult{}, err
	}
	if !s.BucketExists(bucket) {
		return PutResult{}, ErrNoSuchBucket
	}
	if isDirKey(key) {
		return s.putDirMarker(bucket, key, r, opt)
	}
	if opt.MaxSize > 0 && opt.Size > opt.MaxSize {
		return PutResult{}, ErrEntityTooLarge
	}

	f, err := s.newTemp()
	if err != nil {
		return PutResult{}, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()

	h := md5.New()
	limit := opt.MaxSize
	if opt.Size >= 0 {
		limit = opt.Size
	}
	src := r
	if limit > 0 || opt.Size == 0 {
		src = io.LimitReader(r, limit+1)
	}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		return PutResult{}, err
	}
	if opt.Size >= 0 && n < opt.Size {
		return PutResult{}, ErrIncompleteBody
	}
	if (opt.Size >= 0 && n > opt.Size) || (opt.MaxSize > 0 && n > opt.MaxSize) {
		return PutResult{}, ErrEntityTooLarge
	}
	sum := h.Sum(nil)
	if opt.ContentMD5 != "" {
		want, err := base64.StdEncoding.DecodeString(opt.ContentMD5)
		if err != nil || !bytes.Equal(want, sum) {
			return PutResult{}, ErrBadDigest
		}
	}
	if err := f.Sync(); err != nil {
		return PutResult{}, err
	}
	if err := f.Close(); err != nil {
		return PutResult{}, err
	}
	ok = true
	meta := metaFromOptions(key, opt)
	meta.ETag = hex.EncodeToString(sum)
	meta.Size = n
	return s.commit(bucket, key, tmp, meta)
}

func metaFromOptions(key string, opt PutOptions) ObjectMeta {
	ct := opt.ContentType
	if ct == "" {
		ct = DetectContentType(key)
	}
	return ObjectMeta{
		ContentType:        ct,
		UserMeta:           opt.UserMeta,
		CacheControl:       opt.CacheControl,
		ContentDisposition: opt.ContentDisposition,
		ContentEncoding:    opt.ContentEncoding,
		ContentLanguage:    opt.ContentLanguage,
		Expires:            opt.Expires,
	}
}

// commit moves a finished temp file into place and writes its metadata.
func (s *Store) commit(bucket, key, tmp string, meta ObjectMeta) (PutResult, error) {
	dst := s.dataPath(bucket, key)
	mp := s.metaPath(bucket, key)
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()

	var res PutResult
	if fi, err := os.Lstat(dst); err == nil {
		if fi.IsDir() {
			os.Remove(tmp)
			return res, ErrPathConflict
		}
		res.Replaced = true
		res.OldSize = fi.Size()
	}
	if err := mkdirParents(filepath.Dir(mp)); err != nil {
		os.Remove(tmp)
		return res, err
	}
	if err := renameInto(tmp, dst); err != nil {
		os.Remove(tmp)
		return res, err
	}
	meta.LastModified = time.Now().UTC().Truncate(time.Millisecond)
	b, _ := json.Marshal(meta)
	if err := s.writeFileAtomic(mp, b); err != nil {
		return res, err
	}
	res.Info = ObjectInfo{Bucket: bucket, Key: key, ObjectMeta: meta}
	return res, nil
}

func (s *Store) putDirMarker(bucket, key string, r io.Reader, opt PutOptions) (PutResult, error) {
	if opt.Size > 0 {
		return PutResult{}, ErrFolderNotEmpty
	}
	if n, _ := io.Copy(io.Discard, io.LimitReader(r, 1)); n > 0 {
		return PutResult{}, ErrFolderNotEmpty
	}
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()
	dir := s.dataPath(bucket, key)
	if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
		return PutResult{}, ErrPathConflict
	}
	if err := mkdirParents(dir); err != nil {
		return PutResult{}, err
	}
	var res PutResult
	if _, err := os.Stat(s.metaPath(bucket, key)); err == nil {
		res.Replaced = true
	}
	meta := metaFromOptions(key, opt)
	if opt.ContentType == "" {
		meta.ContentType = "application/x-directory"
	}
	meta.ETag = "d41d8cd98f00b204e9800998ecf8427e" // MD5 of empty input
	meta.LastModified = time.Now().UTC().Truncate(time.Millisecond)
	b, _ := json.Marshal(meta)
	if err := s.writeFileAtomic(s.metaPath(bucket, key), b); err != nil {
		return PutResult{}, err
	}
	res.Info = ObjectInfo{Bucket: bucket, Key: key, ObjectMeta: meta}
	return res, nil
}

// StatObject returns an object's metadata.
func (s *Store) StatObject(bucket, key string) (ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, ErrNoSuchKey
	}
	if !s.BucketExists(bucket) {
		return ObjectInfo{}, ErrNoSuchBucket
	}
	l := s.lock(bucket, key)
	l.RLock()
	info, err := s.statLocked(bucket, key)
	l.RUnlock()
	if errors.Is(err, errStaleMeta) {
		return s.refreshMeta(bucket, key)
	}
	return info, err
}

var errStaleMeta = errors.New("metadata missing or stale")

func (s *Store) statLocked(bucket, key string) (ObjectInfo, error) {
	info := ObjectInfo{Bucket: bucket, Key: key}
	if isDirKey(key) {
		b, err := os.ReadFile(s.metaPath(bucket, key))
		if err != nil {
			return info, ErrNoSuchKey
		}
		if err := json.Unmarshal(b, &info.ObjectMeta); err != nil {
			return info, ErrNoSuchKey
		}
		return info, nil
	}
	fi, err := os.Stat(s.dataPath(bucket, key))
	if err != nil || fi.IsDir() {
		return info, ErrNoSuchKey
	}
	b, err := os.ReadFile(s.metaPath(bucket, key))
	if err != nil || json.Unmarshal(b, &info.ObjectMeta) != nil || staleMeta(info.ObjectMeta, fi) {
		return info, errStaleMeta
	}
	return info, nil
}

func staleMeta(m ObjectMeta, fi os.FileInfo) bool {
	return m.ETag == "" || m.Size != fi.Size() || fi.ModTime().After(m.LastModified.Add(2*time.Second))
}

// refreshMeta rebuilds metadata for a file that was placed on disk without
// going through Tijori (for example restored with rsync).
func (s *Store) refreshMeta(bucket, key string) (ObjectInfo, error) {
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()
	info, err := s.statLocked(bucket, key)
	if !errors.Is(err, errStaleMeta) {
		return info, err
	}
	p := s.dataPath(bucket, key)
	f, err := os.Open(p)
	if err != nil {
		return info, ErrNoSuchKey
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return info, err
	}
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return info, err
	}
	old := info.ObjectMeta
	info.ObjectMeta = ObjectMeta{
		ContentType:  DetectContentType(key),
		ETag:         hex.EncodeToString(h.Sum(nil)),
		Size:         fi.Size(),
		LastModified: fi.ModTime().UTC().Truncate(time.Millisecond),
		UserMeta:     old.UserMeta,
	}
	if old.ContentType != "" {
		info.ContentType = old.ContentType
	}
	b, _ := json.Marshal(info.ObjectMeta)
	_ = s.writeFileAtomic(s.metaPath(bucket, key), b)
	return info, nil
}

// ReadSeekCloser is returned by GetObject.
type ReadSeekCloser interface {
	io.ReadSeeker
	io.ReaderAt
	io.Closer
}

type emptyBody struct{ *bytes.Reader }

func (emptyBody) Close() error { return nil }

// GetObject opens an object for reading. The caller must close the reader.
func (s *Store) GetObject(bucket, key string) (ReadSeekCloser, ObjectInfo, error) {
	info, err := s.StatObject(bucket, key)
	if err != nil {
		return nil, info, err
	}
	if isDirKey(key) {
		return emptyBody{bytes.NewReader(nil)}, info, nil
	}
	l := s.lock(bucket, key)
	l.RLock()
	defer l.RUnlock()
	f, err := os.Open(s.dataPath(bucket, key))
	if err != nil {
		return nil, info, ErrNoSuchKey
	}
	// Re-read metadata under the lock so data and metadata match even if
	// the object was replaced between StatObject and Open.
	if fresh, err := s.statLocked(bucket, key); err == nil {
		info = fresh
	}
	return f, info, nil
}

// DeleteObject removes an object. Deleting a missing key is not an error.
// It returns the size of the removed object and whether it existed.
func (s *Store) DeleteObject(bucket, key string) (int64, bool, error) {
	if err := ValidateKey(key); err != nil {
		return 0, false, nil
	}
	if !s.BucketExists(bucket) {
		return 0, false, ErrNoSuchBucket
	}
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()
	if isDirKey(key) {
		mp := s.metaPath(bucket, key)
		if err := os.Remove(mp); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return 0, false, nil
			}
			return 0, false, err
		}
		s.prune(bucket, key)
		return 0, true, nil
	}
	p := s.dataPath(bucket, key)
	fi, err := os.Lstat(p)
	if err != nil || fi.IsDir() {
		return 0, false, nil
	}
	if err := os.Remove(p); err != nil {
		return 0, false, err
	}
	os.Remove(s.metaPath(bucket, key))
	s.prune(bucket, path.Dir(key)+"/")
	return fi.Size(), true, nil
}

// prune removes now-empty parent directories of a deleted key, in both the
// data and meta trees, stopping at folder markers and the bucket root.
func (s *Store) prune(bucket, dirKey string) {
	for dirKey != "./" && dirKey != "/" && dirKey != "" {
		if _, err := os.Stat(s.metaPath(bucket, dirKey)); err == nil {
			return // explicit folder marker keeps the folder
		}
		rel := strings.TrimSuffix(dirKey, "/")
		if os.Remove(safeJoin(s.bucketDir(bucket), rel)) != nil {
			return
		}
		os.Remove(safeJoin(s.bucketMetaDir(bucket), rel))
		dirKey = path.Dir(rel) + "/"
	}
}

// ---- Listing ----------------------------------------------------------------

// ListOptions controls ListObjects.
type ListOptions struct {
	Prefix    string
	Delimiter string
	Marker    string // return keys strictly after this
	MaxKeys   int
	// FetchMeta loads ETag/content type for every object. When false only
	// size and modification time from the filesystem are returned.
	FetchMeta bool
}

// ListResult is the outcome of ListObjects.
type ListResult struct {
	Objects        []ObjectInfo
	CommonPrefixes []string
	IsTruncated    bool
	NextMarker     string
}

var errStopWalk = errors.New("stop")

// ListObjects lists keys in S3 order (byte-wise lexicographic).
func (s *Store) ListObjects(bucket string, opt ListOptions) (ListResult, error) {
	var res ListResult
	if !s.BucketExists(bucket) {
		return res, ErrNoSuchBucket
	}
	if opt.MaxKeys <= 0 {
		return res, nil
	}
	count := 0
	lastCP := ""
	emit := func(obj *ObjectInfo, cp string) error {
		if count >= opt.MaxKeys {
			res.IsTruncated = true
			return errStopWalk
		}
		count++
		if obj != nil {
			res.Objects = append(res.Objects, *obj)
			res.NextMarker = obj.Key
		} else {
			res.CommonPrefixes = append(res.CommonPrefixes, cp)
			res.NextMarker = cp
			lastCP = cp
		}
		return nil
	}
	rollup := func(key string) (string, bool) {
		if opt.Delimiter == "" || !strings.HasPrefix(key, opt.Prefix) {
			return "", false
		}
		rest := key[len(opt.Prefix):]
		if i := strings.Index(rest, opt.Delimiter); i >= 0 {
			return opt.Prefix + rest[:i+len(opt.Delimiter)], true
		}
		return "", false
	}

	var visit func(dirKey string) error
	visit = func(dirKey string) error {
		// The folder marker for this directory sorts before its children.
		if dirKey != "" && strings.HasPrefix(dirKey, opt.Prefix) && dirKey > opt.Marker {
			if info, err := s.StatObject(bucket, dirKey); err == nil {
				if err := emit(&info, ""); err != nil {
					return err
				}
			}
		}
		dir := safeJoin(s.bucketDir(bucket), strings.TrimSuffix(dirKey, "/"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return nil
			}
			return err
		}
		type ent struct {
			key   string
			isDir bool
		}
		list := make([]ent, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(strings.ToLower(name), reservedPrefix) {
				continue
			}
			isDir := e.IsDir()
			if e.Type()&fs.ModeSymlink != 0 {
				if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
					isDir = fi.IsDir()
				}
			}
			k := dirKey + name
			if isDir {
				k += "/"
			}
			list = append(list, ent{k, isDir})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].key < list[j].key })
		for _, e := range list {
			if e.isDir {
				if !strings.HasPrefix(e.key, opt.Prefix) && !strings.HasPrefix(opt.Prefix, e.key) {
					continue
				}
				// Whole subtree sorts before the marker.
				if opt.Marker != "" && e.key < opt.Marker && !strings.HasPrefix(opt.Marker, e.key) {
					continue
				}
				if cp, ok := rollup(e.key); ok {
					if cp > opt.Marker && cp != lastCP {
						if err := emit(nil, cp); err != nil {
							return err
						}
					}
					continue
				}
				if err := visit(e.key); err != nil {
					return err
				}
				continue
			}
			if !strings.HasPrefix(e.key, opt.Prefix) || e.key <= opt.Marker {
				continue
			}
			if cp, ok := rollup(e.key); ok {
				if cp > opt.Marker && cp != lastCP {
					if err := emit(nil, cp); err != nil {
						return err
					}
				}
				continue
			}
			info, err := s.listInfo(bucket, e.key, opt.FetchMeta)
			if err != nil {
				continue // vanished while listing
			}
			if err := emit(&info, ""); err != nil {
				return err
			}
		}
		return nil
	}

	start := ""
	if i := strings.LastIndex(opt.Prefix, "/"); i >= 0 {
		start = opt.Prefix[:i+1]
	}
	if err := visit(start); err != nil && !errors.Is(err, errStopWalk) {
		return res, err
	}
	if !res.IsTruncated {
		res.NextMarker = ""
	}
	return res, nil
}

func (s *Store) listInfo(bucket, key string, fetchMeta bool) (ObjectInfo, error) {
	if fetchMeta {
		return s.StatObject(bucket, key)
	}
	fi, err := os.Stat(s.dataPath(bucket, key))
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Bucket: bucket, Key: key, ObjectMeta: ObjectMeta{
		Size: fi.Size(), LastModified: fi.ModTime().UTC(),
	}}, nil
}

// WalkObjects calls fn for every object in bucket, in key order, without
// loading metadata (size and mtime come from the filesystem).
func (s *Store) WalkObjects(bucket string, fn func(ObjectInfo) error) error {
	marker := ""
	for {
		res, err := s.ListObjects(bucket, ListOptions{Marker: marker, MaxKeys: 1000})
		if err != nil {
			return err
		}
		for _, o := range res.Objects {
			if err := fn(o); err != nil {
				return err
			}
		}
		if !res.IsTruncated {
			return nil
		}
		marker = res.NextMarker
	}
}

// BucketUsage computes the total size and object count of a bucket.
func (s *Store) BucketUsage(bucket string) (bytes, objects int64, err error) {
	err = s.WalkObjects(bucket, func(o ObjectInfo) error {
		bytes += o.Size
		objects++
		return nil
	})
	return
}

// DataPath exposes the on-disk path of an object for read-only consumers
// such as ffmpeg. The key must be valid.
func (s *Store) DataPath(bucket, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return s.dataPath(bucket, key), nil
}

// TempFile creates a scratch file inside the data directory so that a later
// rename into place stays on the same filesystem.
func (s *Store) TempFile() (*os.File, error) { return s.newTemp() }

// CopyObject copies an object. When opt is nil the source metadata is kept;
// otherwise opt replaces it. Copying an object onto itself with new metadata
// only rewrites the metadata.
func (s *Store) CopyObject(srcBucket, srcKey, dstBucket, dstKey string, opt *PutOptions, maxSize int64) (PutResult, error) {
	if err := ValidateKey(dstKey); err != nil {
		return PutResult{}, err
	}
	if srcBucket == dstBucket && srcKey == dstKey && opt != nil {
		return s.replaceMeta(srcBucket, srcKey, *opt)
	}
	src, info, err := s.GetObject(srcBucket, srcKey)
	if err != nil {
		return PutResult{}, err
	}
	defer src.Close()
	o := PutOptions{
		ContentType:        info.ContentType,
		UserMeta:           info.UserMeta,
		CacheControl:       info.CacheControl,
		ContentDisposition: info.ContentDisposition,
		ContentEncoding:    info.ContentEncoding,
		ContentLanguage:    info.ContentLanguage,
		Expires:            info.Expires,
	}
	if opt != nil {
		o = *opt
	}
	o.Size = info.Size
	o.MaxSize = maxSize
	o.ContentMD5 = ""
	return s.PutObject(dstBucket, dstKey, src, o)
}

func (s *Store) replaceMeta(bucket, key string, opt PutOptions) (PutResult, error) {
	info, err := s.StatObject(bucket, key)
	if err != nil {
		return PutResult{}, err
	}
	l := s.lock(bucket, key)
	l.Lock()
	defer l.Unlock()
	m := metaFromOptions(key, opt)
	m.ETag, m.Size, m.Parts = info.ETag, info.Size, info.Parts
	m.LastModified = time.Now().UTC().Truncate(time.Millisecond)
	if !isDirKey(key) {
		// Keep the staleness check happy: data mtime must not be newer.
		now := time.Now()
		_ = os.Chtimes(s.dataPath(bucket, key), now, now)
	}
	b, _ := json.Marshal(m)
	if err := s.writeFileAtomic(s.metaPath(bucket, key), b); err != nil {
		return PutResult{}, err
	}
	return PutResult{Info: ObjectInfo{Bucket: bucket, Key: key, ObjectMeta: m}, Replaced: true, OldSize: info.Size}, nil
}

// MetaFile returns the metadata file of an object (for backups).
func (s *Store) MetaFile(bucket, key string) string { return s.metaPath(bucket, key) }

// BucketMetaFile returns the bucket's own metadata file (for backups).
func (s *Store) BucketMetaFile(bucket string) string {
	return filepath.Join(s.bucketMetaDir(bucket), bucketMetaName)
}
