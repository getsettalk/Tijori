package storage

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrNoSuchUpload     = errors.New("the specified multipart upload does not exist")
	ErrInvalidPart      = errors.New("one or more of the specified parts could not be found or the ETag did not match")
	ErrInvalidPartOrder = errors.New("the list of parts was not in ascending order")
	ErrEntityTooSmall   = errors.New("your proposed upload is smaller than the minimum allowed size (5 MiB for all parts except the last)")
	ErrInvalidPartNum   = errors.New("part number must be an integer between 1 and 10000")
)

// MinPartSize is S3's minimum size for every part except the last.
const MinPartSize = 5 << 20

// Upload describes an in-progress multipart upload.
type Upload struct {
	ID        string     `json:"id"`
	Bucket    string     `json:"bucket"`
	Key       string     `json:"key"`
	Initiated time.Time  `json:"initiated"`
	Options   PutOptions `json:"options"`
}

// Part describes one uploaded part.
type Part struct {
	Number       int       `json:"number"`
	ETag         string    `json:"etag"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// CompletePart is a part reference sent by the client on completion.
type CompletePart struct {
	Number int
	ETag   string
}

func (s *Store) uploadDir(id string) string {
	return filepath.Join(s.root, ".multipart", id)
}

func validUploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// CreateMultipartUpload starts a new multipart upload.
func (s *Store) CreateMultipartUpload(bucket, key string, opt PutOptions) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if isDirKey(key) {
		return "", ErrFolderNotEmpty
	}
	if !s.BucketExists(bucket) {
		return "", ErrNoSuchBucket
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	opt.ContentMD5 = ""
	up := Upload{ID: id, Bucket: bucket, Key: key, Initiated: time.Now().UTC(), Options: opt}
	if err := os.MkdirAll(s.uploadDir(id), 0o755); err != nil {
		return "", err
	}
	data, _ := json.Marshal(up)
	if err := s.writeFileAtomic(filepath.Join(s.uploadDir(id), "upload.json"), data); err != nil {
		return "", err
	}
	return id, nil
}

// GetUpload loads an upload and checks it belongs to bucket/key.
func (s *Store) GetUpload(bucket, key, id string) (Upload, error) {
	var up Upload
	if !validUploadID(id) {
		return up, ErrNoSuchUpload
	}
	b, err := os.ReadFile(filepath.Join(s.uploadDir(id), "upload.json"))
	if err != nil {
		return up, ErrNoSuchUpload
	}
	if err := json.Unmarshal(b, &up); err != nil || up.Bucket != bucket || up.Key != key {
		return up, ErrNoSuchUpload
	}
	return up, nil
}

func partFile(n int) string { return fmt.Sprintf("part-%05d", n) }

// UploadPart stores one part. size is the expected length (-1 if unknown).
func (s *Store) UploadPart(bucket, key, id string, number int, r io.Reader, size int64, contentMD5 string, maxSize int64) (Part, error) {
	if number < 1 || number > 10000 {
		return Part{}, ErrInvalidPartNum
	}
	if _, err := s.GetUpload(bucket, key, id); err != nil {
		return Part{}, err
	}
	f, err := s.newTemp()
	if err != nil {
		return Part{}, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()

	h := md5.New()
	src := r
	if size >= 0 {
		src = io.LimitReader(r, size+1)
	} else if maxSize > 0 {
		src = io.LimitReader(r, maxSize+1)
	}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		return Part{}, err
	}
	if size >= 0 && n < size {
		return Part{}, ErrIncompleteBody
	}
	if (size >= 0 && n > size) || (maxSize > 0 && n > maxSize) {
		return Part{}, ErrEntityTooLarge
	}
	sum := h.Sum(nil)
	if contentMD5 != "" {
		want, err := base64.StdEncoding.DecodeString(contentMD5)
		if err != nil || !bytes.Equal(want, sum) {
			return Part{}, ErrBadDigest
		}
	}
	if err := f.Sync(); err != nil {
		return Part{}, err
	}
	if err := f.Close(); err != nil {
		return Part{}, err
	}
	p := Part{Number: number, ETag: hex.EncodeToString(sum), Size: n, LastModified: time.Now().UTC().Truncate(time.Millisecond)}
	dir := s.uploadDir(id)
	if _, err := os.Stat(dir); err != nil {
		return Part{}, ErrNoSuchUpload // aborted meanwhile
	}
	if err := os.Rename(tmp, filepath.Join(dir, partFile(number))); err != nil {
		return Part{}, err
	}
	b, _ := json.Marshal(p)
	if err := s.writeFileAtomic(filepath.Join(dir, partFile(number)+".json"), b); err != nil {
		return Part{}, err
	}
	return p, nil
}

// ListParts returns uploaded parts in order.
func (s *Store) ListParts(bucket, key, id string) ([]Part, error) {
	if _, err := s.GetUpload(bucket, key, id); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.uploadDir(id))
	if err != nil {
		return nil, ErrNoSuchUpload
	}
	var parts []Part
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "part-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.uploadDir(id), e.Name()))
		if err != nil {
			continue
		}
		var p Part
		if json.Unmarshal(b, &p) == nil {
			parts = append(parts, p)
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	return parts, nil
}

// PendingBytes is the total size of parts uploaded so far.
func (s *Store) PendingBytes(bucket, key, id string) int64 {
	parts, _ := s.ListParts(bucket, key, id)
	var n int64
	for _, p := range parts {
		n += p.Size
	}
	return n
}

// CompleteMultipartUpload validates the part list, assembles the object and
// moves it into place. The ETag is the MD5 of the part MD5s plus "-N".
func (s *Store) CompleteMultipartUpload(bucket, key, id string, want []CompletePart, check func(total int64) error) (PutResult, error) {
	up, err := s.GetUpload(bucket, key, id)
	if err != nil {
		return PutResult{}, err
	}
	have, err := s.ListParts(bucket, key, id)
	if err != nil {
		return PutResult{}, err
	}
	if len(want) == 0 {
		return PutResult{}, ErrInvalidPart
	}
	byNum := make(map[int]Part, len(have))
	for _, p := range have {
		byNum[p.Number] = p
	}
	var total int64
	md5s := md5.New()
	for i := 1; i < len(want); i++ {
		if want[i].Number <= want[i-1].Number {
			return PutResult{}, ErrInvalidPartOrder
		}
	}
	for i, w := range want {
		p, ok := byNum[w.Number]
		if !ok || strings.Trim(w.ETag, `"`) != p.ETag {
			return PutResult{}, ErrInvalidPart
		}
		if i < len(want)-1 && p.Size < MinPartSize {
			return PutResult{}, ErrEntityTooSmall
		}
		raw, _ := hex.DecodeString(p.ETag)
		md5s.Write(raw)
		total += p.Size
	}
	if check != nil {
		if err := check(total); err != nil {
			return PutResult{}, err
		}
	}

	out, err := s.newTemp()
	if err != nil {
		return PutResult{}, err
	}
	tmp := out.Name()
	fail := func(err error) (PutResult, error) {
		out.Close()
		os.Remove(tmp)
		return PutResult{}, err
	}
	for _, w := range want {
		pf, err := os.Open(filepath.Join(s.uploadDir(id), partFile(w.Number)))
		if err != nil {
			return fail(ErrInvalidPart)
		}
		_, err = io.Copy(out, pf)
		pf.Close()
		if err != nil {
			return fail(err)
		}
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		return fail(err)
	}
	meta := metaFromOptions(key, up.Options)
	meta.ETag = fmt.Sprintf("%s-%d", hex.EncodeToString(md5s.Sum(nil)), len(want))
	meta.Size = total
	meta.Parts = len(want)
	res, err := s.commit(bucket, key, tmp, meta)
	if err != nil {
		return res, err
	}
	_ = os.RemoveAll(s.uploadDir(id))
	return res, nil
}

// AbortMultipartUpload discards an upload and its parts.
func (s *Store) AbortMultipartUpload(bucket, key, id string) error {
	if _, err := s.GetUpload(bucket, key, id); err != nil {
		return err
	}
	return os.RemoveAll(s.uploadDir(id))
}

// ListUploads returns in-progress uploads for a bucket (all buckets if "").
func (s *Store) ListUploads(bucket, prefix string) ([]Upload, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, ".multipart"))
	if err != nil {
		return nil, err
	}
	var out []Upload
	for _, e := range entries {
		if !e.IsDir() || !validUploadID(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.uploadDir(e.Name()), "upload.json"))
		if err != nil {
			continue
		}
		var up Upload
		if json.Unmarshal(b, &up) != nil {
			continue
		}
		if (bucket == "" || up.Bucket == bucket) && strings.HasPrefix(up.Key, prefix) {
			out = append(out, up)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Initiated.Before(out[j].Initiated)
	})
	return out, nil
}

// CleanStaleUploads aborts uploads older than maxAge.
func (s *Store) CleanStaleUploads(maxAge time.Duration) int {
	ups, _ := s.ListUploads("", "")
	n := 0
	for _, up := range ups {
		if time.Since(up.Initiated) > maxAge {
			if os.RemoveAll(s.uploadDir(up.ID)) == nil {
				n++
			}
		}
	}
	s.cleanTmp(24 * time.Hour)
	return n
}
