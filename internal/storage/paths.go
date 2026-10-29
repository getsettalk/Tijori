package storage

import (
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// Reserved names used for Tijori's own files inside the meta tree. Any key
// component starting with ".tijori" is rejected so it can never collide.
const (
	reservedPrefix = ".tijori"
	dirMarkerName  = ".tijori-dir.json"
	bucketMetaName = ".tijori-bucket.json"
	metaSuffix     = ".json"
	maxKeyLen      = 1024
	maxComponent   = 255
)

// ValidateKey checks that an object key is safe to map onto the filesystem.
// Keys may end in "/" (folder markers) but may not contain empty, "." or ".."
// components, backslashes, NUL bytes or invalid UTF-8.
func ValidateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}
	if len(key) > maxKeyLen {
		return ErrKeyTooLong
	}
	if !utf8.ValidString(key) || strings.ContainsAny(key, "\x00\\") || strings.HasPrefix(key, "/") {
		return ErrInvalidKey
	}
	parts := strings.Split(key, "/")
	for i, p := range parts {
		if p == "" {
			// Only a single trailing slash is allowed (folder marker).
			if i == len(parts)-1 && i > 0 {
				continue
			}
			return ErrInvalidKey
		}
		if p == "." || p == ".." || strings.HasPrefix(p, reservedPrefix) {
			return ErrInvalidKey
		}
		if len(p) > maxComponent {
			return ErrKeyTooLong
		}
		if windowsPaths && !windowsSafe(p) {
			return ErrInvalidKey
		}
	}
	return nil
}

// windowsPaths is true when keys must also be valid Windows file names.
var windowsPaths = runtime.GOOS == "windows"

var windowsReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}

// windowsSafe rejects names Windows treats specially: device names such as
// CON or COM1 (even with an extension), characters like ':' (which would
// address an alternate data stream), control characters, and names ending
// in a space or dot.
func windowsSafe(name string) bool {
	if strings.ContainsAny(name, `<>:"|?*`) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	if windowsReserved[base] {
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
		return false
	}
	return true
}

// isDirKey reports whether key is a folder marker such as "photos/".
func isDirKey(key string) bool { return strings.HasSuffix(key, "/") }

func (s *Store) bucketDir(bucket string) string {
	return filepath.Join(s.root, "buckets", bucket)
}

func (s *Store) bucketMetaDir(bucket string) string {
	return filepath.Join(s.root, "meta", bucket)
}

// dataPath maps a validated key to its file. Folder markers map to the
// directory itself.
func (s *Store) dataPath(bucket, key string) string {
	return safeJoin(s.bucketDir(bucket), strings.TrimSuffix(key, "/"))
}

func (s *Store) metaPath(bucket, key string) string {
	if isDirKey(key) {
		return filepath.Join(safeJoin(s.bucketMetaDir(bucket), strings.TrimSuffix(key, "/")), dirMarkerName)
	}
	return safeJoin(s.bucketMetaDir(bucket), key) + metaSuffix
}

// safeJoin joins base and a slash-separated relative path and guarantees the
// result stays inside base. ValidateKey already rejects traversal; this is
// defence in depth.
func safeJoin(base, rel string) string {
	p := filepath.Join(base, filepath.FromSlash(rel))
	r, err := filepath.Rel(base, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		// Should be unreachable for validated keys; point at a path that
		// cannot exist rather than escaping.
		return filepath.Join(base, ".tijori-invalid")
	}
	return p
}
