package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/getsettalk/Tijori/internal/backup"
	"github.com/getsettalk/Tijori/internal/state"
)

// checksums hashes every file in a data dir's buckets and meta trees.
func checksums(t *testing.T, dataDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, sub := range []string{"buckets", "meta"} {
		root := filepath.Join(dataDir, sub)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, _ := os.ReadFile(p)
			sum := sha256.Sum256(b)
			rel, _ := filepath.Rel(dataDir, p)
			out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
			return nil
		})
	}
	return out
}

func seed(t *testing.T, e *testEnv) {
	t.Helper()
	for _, b := range []string{"photos", "docs"} {
		if _, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(b)}); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"photos/a.jpg": "aaa", "photos/2024/b.jpg": strings.Repeat("b", 70000), "docs/x/y/z.txt": "zzz", "docs/folder/": ""}
	for k, v := range files {
		b, key, _ := strings.Cut(k, "/")
		if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b), Key: aws.String(key), Body: strings.NewReader(v),
			ContentType: aws.String("application/x-test"), Metadata: map[string]string{"k": "v"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func compareDirs(t *testing.T, a, b string) {
	t.Helper()
	ca, cb := checksums(t, a), checksums(t, b)
	if len(ca) == 0 || len(ca) != len(cb) {
		t.Fatalf("file count differs: %d vs %d\n%v\n%v", len(ca), len(cb), ca, cb)
	}
	for k, v := range ca {
		if cb[k] != v {
			t.Errorf("%s differs after restore", k)
		}
	}
}

func TestBackupToFolderAndRestore(t *testing.T) {
	e := startServer(t, nil)
	seed(t, e)
	dest := t.TempDir()
	tg, _ := backup.Open(dest)
	st, err := backup.Run(e.srv.Store, e.srv.DB, tg, backup.Options{Keep: 2})
	if err != nil || st.Copied != 4 {
		t.Fatalf("first backup: %v %+v", err, st)
	}
	st, err = backup.Run(e.srv.Store, e.srv.DB, tg, backup.Options{Keep: 2})
	if err != nil || st.Copied != 0 || st.Skipped != 4 {
		t.Fatalf("incremental backup should copy nothing: %v %+v", err, st)
	}

	fresh := t.TempDir()
	res, err := backup.Restore(tg, fresh, "", nil)
	if err != nil || res.Files != 4 || res.Snapshot == "" {
		t.Fatalf("restore: %v %+v", err, res)
	}
	compareDirs(t, e.srv.Cfg.DataDir, fresh)
	db, err := state.Open(fresh)
	if err != nil || len(db.Keys()) != 1 {
		t.Fatalf("keys not restored: %v", err)
	}

	// A deleted object survives `keep` backups, then disappears.
	if _, err := e.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("photos"), Key: aws.String("a.jpg")}); err != nil {
		t.Fatal(err)
	}
	backedUp := filepath.Join(dest, "data", "buckets", "photos", "a.jpg")
	_, _ = backup.Run(e.srv.Store, e.srv.DB, tg, backup.Options{Keep: 2})
	if _, err := os.Stat(backedUp); err != nil {
		t.Fatal("deleted file removed from backup too early")
	}
	_, _ = backup.Run(e.srv.Store, e.srv.DB, tg, backup.Options{Keep: 2})
	if _, err := os.Stat(backedUp); !os.IsNotExist(err) {
		t.Fatal("deleted file kept beyond the grace period")
	}
	if snaps, _ := os.ReadDir(filepath.Join(dest, "snapshots")); len(snaps) > 2 {
		t.Fatalf("keep=2 but %d snapshots remain", len(snaps))
	}
}

func TestBackupToAnotherTijori(t *testing.T) {
	src := startServer(t, nil)
	seed(t, src)
	dst := startServer(t, nil)
	if _, err := dst.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("backups")}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIJORI_BACKUP_ACCESS_KEY", dst.key.AccessKey)
	t.Setenv("TIJORI_BACKUP_SECRET_KEY", dst.key.SecretKey)
	tg, err := backup.Open(fmt.Sprintf("s3://backups/server1?endpoint=%s", dst.url))
	if err != nil {
		t.Fatal(err)
	}
	if st, err := backup.Run(src.srv.Store, src.srv.DB, tg, backup.Options{Keep: 3}); err != nil || st.Copied != 4 {
		t.Fatalf("s3 backup: %v %+v", err, st)
	}
	if st, _ := backup.Run(src.srv.Store, src.srv.DB, tg, backup.Options{Keep: 3}); st.Copied != 0 {
		t.Fatalf("s3 incremental copied %d", st.Copied)
	}
	fresh := t.TempDir()
	if _, err := backup.Restore(tg, fresh, "", nil); err != nil {
		t.Fatal(err)
	}
	compareDirs(t, src.srv.Cfg.DataDir, fresh)
}

func TestExportImport(t *testing.T) {
	e := startServer(t, nil)
	seed(t, e)
	var buf bytes.Buffer
	info, err := backup.Export(&buf, e.srv.Cfg.DataDir, "", "test")
	if err != nil || info.Files == 0 {
		t.Fatalf("export: %v %+v", err, info)
	}
	fresh := t.TempDir()
	if _, err := backup.Import(bytes.NewReader(buf.Bytes()), fresh, "", false); err != nil {
		t.Fatal(err)
	}
	compareDirs(t, e.srv.Cfg.DataDir, fresh)
	if fi, err := os.Stat(filepath.Join(fresh, state.FileName)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("db not imported with 0600: %v", err)
	}
	// Importing over existing buckets needs --force.
	if _, err := backup.Import(bytes.NewReader(buf.Bytes()), fresh, "", false); err == nil {
		t.Fatal("import over existing data should be refused")
	}
}

func TestImportRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"tijori-export/data/..\\..\\evil", "tijori-export/data/../../evil", "/abs/evil"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("x"))
		tw.Close()
		gz.Close()
		if _, err := backup.Import(&buf, t.TempDir(), "", true); err == nil {
			t.Errorf("import accepted %q", name)
		}
	}
}
