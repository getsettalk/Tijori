package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const archiveRoot = "tijori-export"

var dataDirLine = regexp.MustCompile(`(?m)^data_dir\s*=.*$`)

// skipInExport lists data-dir entries that are temporary or rebuildable.
var skipInExport = map[string]bool{".tmp": true, ".cache": true, ".multipart": true, ".jobs": true}

// ExportInfo is stored as tijori-export/export.json.
type ExportInfo struct {
	Created time.Time `json:"created"`
	Version string    `json:"version"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
}

// Export writes dataDir (and configFile, if set) as a .tar.gz to w.
func Export(w io.Writer, dataDir, configFile, version string) (ExportInfo, error) {
	info := ExportInfo{Created: time.Now().UTC(), Version: version}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	addFile := func(name, src string, fi fs.FileInfo) error {
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := io.Copy(tw, f)
		info.Files++
		info.Bytes += n
		return err
	}
	err := filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dataDir, p)
		if rel == "." {
			return nil
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		if skipInExport[top] || strings.HasPrefix(d.Name(), ".tmp-") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := archiveRoot + "/data/" + filepath.ToSlash(rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			hdr, _ := tar.FileInfoHeader(fi, "")
			hdr.Name = name + "/"
			hdr.Uname, hdr.Gname = "", ""
			return tw.WriteHeader(hdr)
		}
		if !fi.Mode().IsRegular() {
			return nil // skip symlinks and special files
		}
		return addFile(name, p, fi)
	})
	if err != nil {
		return info, err
	}
	if configFile != "" {
		fi, err := os.Stat(configFile)
		if err != nil {
			return info, err
		}
		if err := addFile(archiveRoot+"/tijori.toml", configFile, fi); err != nil {
			return info, err
		}
	}
	meta, _ := json.MarshalIndent(info, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: archiveRoot + "/export.json", Mode: 0o644, Size: int64(len(meta)), ModTime: info.Created, Typeflag: tar.TypeReg}); err != nil {
		return info, err
	}
	if _, err := tw.Write(meta); err != nil {
		return info, err
	}
	if err := tw.Close(); err != nil {
		return info, err
	}
	return info, gz.Close()
}

// ImportResult describes an import.
type ImportResult struct {
	Files      int
	Bytes      int64
	ConfigFile string // where the archived tijori.toml was written
}

// Import unpacks an export into dataDir. dataDir must not already contain
// buckets unless force is set. The archived config is written to
// configDest when that file does not exist, otherwise next to it with an
// ".imported" suffix.
func Import(r io.Reader, dataDir, configDest string, force bool) (ImportResult, error) {
	var res ImportResult
	if !force {
		if entries, err := os.ReadDir(filepath.Join(dataDir, "buckets")); err == nil && len(entries) > 0 {
			return res, fmt.Errorf("%s already has buckets; import into an empty folder or add --force to overwrite", dataDir)
		}
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return res, fmt.Errorf("not a Tijori export (.tar.gz): %w", err)
	}
	tr := tar.NewReader(gz)
	sawData := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, err
		}
		name := path.Clean(hdr.Name)
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return res, fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		rest, ok := strings.CutPrefix(name, archiveRoot+"/")
		if !ok {
			continue
		}
		var dst string
		var perm os.FileMode = 0o644
		switch {
		case rest == "tijori.toml":
			if configDest == "" {
				continue
			}
			dst = configDest
			if _, err := os.Stat(dst); err == nil {
				dst += ".imported"
			}
			perm = 0o600
			res.ConfigFile = dst
		case strings.HasPrefix(rest, "data/"):
			sawData = true
			rel := strings.TrimPrefix(rest, "data/")
			if rel == "" {
				continue
			}
			dst = filepath.Join(dataDir, filepath.FromSlash(rel))
			if rel == "tijori.db.json" {
				perm = 0o600
			}
		default:
			continue
		}
		if rest == "tijori.toml" {
			// Point the imported config at the folder we unpacked into.
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return res, err
			}
			abs, _ := filepath.Abs(dataDir)
			b = dataDirLine.ReplaceAll(b, []byte(fmt.Sprintf("data_dir = %q", abs)))
			if err := os.WriteFile(dst, b, perm); err != nil {
				return res, err
			}
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return res, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return res, err
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return res, err
			}
			n, err := io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return res, err
			}
			_ = os.Chtimes(dst, hdr.ModTime, hdr.ModTime)
			res.Files++
			res.Bytes += n
		}
	}
	if !sawData {
		return res, errors.New("archive does not contain Tijori data (expected tijori-export/data/)")
	}
	return res, nil
}
