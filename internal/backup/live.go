package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

// Restoring an export (.tar.gz from Export, `tijori export` or the admin
// panel's download) into a running server. CheckArchive reads the whole
// file first, so a damaged or hostile file is refused before anything
// changes; RestoreLive then adds its buckets, files, keys and users.

// Size limits for the small JSON files in an export.
const (
	maxMetaFile = 1 << 20
	maxDBFile   = 64 << 20
)

type entryKind int

const (
	kindSkip       entryKind = iota
	kindBucket               // data/buckets/<bucket>/
	kindObject               // data/buckets/<bucket>/<key>
	kindMeta                 // data/meta/<bucket>/<key>.json or <folder>/.tijori-dir.json
	kindBucketMeta           // data/meta/<bucket>/.tijori-bucket.json
	kindDB                   // data/tijori.db.json
	kindInfo                 // export.json
	kindConfig               // tijori.toml
	kindInvalid              // a file whose bucket or name this server cannot store
)

type entry struct {
	kind        entryKind
	bucket, key string
}

// classify maps an archive entry to what it holds, refusing unsafe names.
func classify(hdr *tar.Header) (entry, error) {
	name := path.Clean(hdr.Name)
	if strings.ContainsAny(hdr.Name, "\\\x00") || strings.HasPrefix(name, "/") || name == ".." ||
		strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return entry{}, fmt.Errorf("unsafe path in backup: %q", hdr.Name)
	}
	rest, ok := strings.CutPrefix(name, archiveRoot+"/")
	if !ok {
		return entry{}, nil
	}
	dir := hdr.Typeflag == tar.TypeDir
	if !dir && hdr.Typeflag != tar.TypeReg {
		return entry{}, nil // links and devices are never restored
	}
	switch rest {
	case "export.json":
		return entry{kind: kindInfo}, nil
	case "tijori.toml":
		return entry{kind: kindConfig}, nil
	case "data/" + state.FileName:
		return entry{kind: kindDB}, nil
	}
	var top string
	var parts []string
	if r, ok := strings.CutPrefix(rest, "data/buckets/"); ok {
		top, parts = "buckets", strings.Split(r, "/")
	} else if r, ok := strings.CutPrefix(rest, "data/meta/"); ok {
		top, parts = "meta", strings.Split(r, "/")
	} else {
		return entry{}, nil // temporary and internal folders
	}
	bucket := parts[0]
	if bucket == "" {
		return entry{}, nil // data/buckets/ itself
	}
	rel := strings.Join(parts[1:], "/")
	if !config.ValidBucketName(bucket) {
		if top == "buckets" && !dir {
			return entry{kind: kindInvalid, bucket: bucket, key: rel}, nil
		}
		return entry{}, nil
	}
	switch {
	case top == "buckets" && rel == "":
		if dir {
			return entry{kind: kindBucket, bucket: bucket}, nil
		}
	case top == "buckets":
		if dir {
			return entry{}, nil // folders inside a bucket are created as needed
		}
		if storage.ValidateKey(rel) != nil {
			// For example a name Windows cannot store, in a backup made on
			// Linux. It is skipped and counted.
			return entry{kind: kindInvalid, bucket: bucket, key: rel}, nil
		}
		return entry{kind: kindObject, bucket: bucket, key: rel}, nil
	case dir:
	case rel == ".tijori-bucket.json":
		return entry{kind: kindBucketMeta, bucket: bucket}, nil
	case path.Base(rel) == ".tijori-dir.json":
		key := path.Dir(rel) + "/"
		if storage.ValidateKey(key) == nil {
			return entry{kind: kindMeta, bucket: bucket, key: key}, nil
		}
	case strings.HasSuffix(rel, ".json"):
		key := strings.TrimSuffix(rel, ".json")
		if storage.ValidateKey(key) == nil {
			return entry{kind: kindMeta, bucket: bucket, key: key}, nil
		}
	}
	return entry{}, nil
}

// walkArchive calls fn for every entry of a gzip-compressed export.
func walkArchive(r io.Reader, fn func(hdr *tar.Header, e entry, body io.Reader) error) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return errors.New("this is not a Tijori backup file (.tar.gz)")
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the backup file is damaged or incomplete: %w", err)
		}
		e, err := classify(hdr)
		if err != nil {
			return err
		}
		if e.kind == kindSkip {
			continue
		}
		if err := fn(hdr, e, tr); err != nil {
			return err
		}
	}
}

// ArchiveInfo describes a backup file.
type ArchiveInfo struct {
	Created time.Time `json:"created"`
	Version string    `json:"version"`
	Buckets []string  `json:"buckets"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	HasKeys bool      `json:"has_keys"`
	Invalid int       `json:"invalid"` // files that cannot be restored here
}

// CheckArchive reads a whole backup file and reports what it holds. It
// fails for damaged, cut-off or unsafe files.
func CheckArchive(r io.Reader) (ArchiveInfo, error) {
	var info ArchiveInfo
	sawInfo := false
	err := walkArchive(r, func(hdr *tar.Header, e entry, body io.Reader) error {
		if e.kind == kindInvalid {
			info.Invalid++
			return nil
		}
		if e.bucket != "" && !slices.Contains(info.Buckets, e.bucket) {
			info.Buckets = append(info.Buckets, e.bucket)
		}
		switch e.kind {
		case kindObject:
			n, err := io.Copy(io.Discard, body)
			if err != nil {
				return fmt.Errorf("the backup file is damaged or incomplete: %w", err)
			}
			info.Files++
			info.Bytes += n
		case kindMeta, kindBucketMeta:
			if hdr.Size > maxMetaFile {
				return fmt.Errorf("metadata for %s/%s is too large", e.bucket, e.key)
			}
		case kindDB:
			if hdr.Size > maxDBFile {
				return errors.New("the database in the backup is too large")
			}
			info.HasKeys = true
		case kindInfo:
			var x ExportInfo
			if json.NewDecoder(io.LimitReader(body, maxMetaFile)).Decode(&x) == nil {
				info.Created, info.Version = x.Created, x.Version
				sawInfo = true
			}
		}
		return nil
	})
	if err != nil {
		return info, err
	}
	if !sawInfo {
		// export.json is the last entry, so a file without it was cut off.
		return info, errors.New("the backup file is incomplete (it ends before its last entry); download it again")
	}
	slices.Sort(info.Buckets)
	return info, nil
}

// LiveResult summarises a restore into a running server.
type LiveResult struct {
	BucketsCreated []string `json:"buckets_created"`
	Files          int      `json:"files"`
	Skipped        int      `json:"skipped"` // already on the server (replace off)
	Invalid        int      `json:"invalid"` // names this server cannot store
	Bytes          int64    `json:"bytes"`
	state.ImportStats
	ConfigIgnored bool `json:"config_ignored"`
}

// RestoreLive adds everything in a backup file to a running server: it
// creates missing buckets and writes files, metadata, bucket settings, keys
// and users. With replace, files and keys that already exist are
// overwritten; without it they are kept. Nothing on the server is deleted.
// progress receives the number of file bytes written so far.
func RestoreLive(r io.Reader, store *storage.Store, db *state.DB, replace bool, progress func(int64)) (LiveResult, error) {
	var res LiveResult
	kept := map[string]bool{} // bucket/key kept in place (replace off)
	created := map[string]bool{}
	ensureBucket := func(b string) error {
		if store.BucketExists(b) {
			return nil
		}
		if err := store.CreateBucket(b); err != nil {
			return fmt.Errorf("creating bucket %s: %w", b, err)
		}
		created[b] = true
		res.BucketsCreated = append(res.BucketsCreated, b)
		return nil
	}
	err := walkArchive(r, func(hdr *tar.Header, e entry, body io.Reader) error {
		switch e.kind {
		case kindInvalid:
			res.Invalid++
		case kindBucket:
			return ensureBucket(e.bucket)
		case kindObject:
			if err := ensureBucket(e.bucket); err != nil {
				return err
			}
			if !replace && store.Exists(e.bucket, e.key) {
				kept[e.bucket+"/"+e.key] = true
				res.Skipped++
				return nil
			}
			n, err := store.RestoreData(e.bucket, e.key, body, hdr.ModTime)
			if err != nil {
				return fmt.Errorf("restoring %s/%s: %w", e.bucket, e.key, err)
			}
			res.Files++
			res.Bytes += n
			if progress != nil {
				progress(res.Bytes)
			}
		case kindMeta:
			if kept[e.bucket+"/"+e.key] {
				return nil
			}
			if err := ensureBucket(e.bucket); err != nil {
				return err
			}
			var m storage.ObjectMeta
			if err := json.NewDecoder(io.LimitReader(body, maxMetaFile)).Decode(&m); err != nil {
				return nil // Tijori rebuilds missing metadata on read
			}
			if strings.HasSuffix(e.key, "/") && !replace && store.Exists(e.bucket, e.key) {
				return nil
			}
			if err := store.RestoreMeta(e.bucket, e.key, m); err != nil {
				return fmt.Errorf("restoring %s/%s: %w", e.bucket, e.key, err)
			}
		case kindBucketMeta:
			if !replace && !created[e.bucket] {
				return nil
			}
			if err := ensureBucket(e.bucket); err != nil {
				return err
			}
			raw, err := io.ReadAll(io.LimitReader(body, maxMetaFile))
			if err != nil {
				return err
			}
			if err := store.RestoreBucketMeta(e.bucket, raw); err != nil {
				return fmt.Errorf("restoring settings of bucket %s: %w", e.bucket, err)
			}
		case kindDB:
			raw, err := io.ReadAll(io.LimitReader(body, maxDBFile))
			if err != nil {
				return err
			}
			st, err := db.ImportFrom(raw, replace)
			if err != nil {
				return err
			}
			res.ImportStats = st
		case kindConfig:
			res.ConfigIgnored = true
		}
		return nil
	})
	return res, err
}
