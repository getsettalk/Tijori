package storage

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("test"); err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s *Store, key, body string) PutResult {
	t.Helper()
	res, err := s.PutObject("test", key, strings.NewReader(body), PutOptions{Size: int64(len(body))})
	if err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
	return res
}

func TestValidateKey(t *testing.T) {
	good := []string{"a", "a/b", "dir/", "a/b/c.txt", "file..txt", "with space", "ünïcødé", ".hidden", "a/.b"}
	bad := []string{"", "/abs", "a//b", "../x", "a/../b", "a/./b", ".", "..", "a\\b", "nul\x00", ".tijori-dir.json", "a/.tijori-x", strings.Repeat("x", 256), strings.Repeat("a/", 600)}
	for _, k := range good {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want ok", k, err)
		}
	}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) = ok, want error", k)
		}
	}
}

func TestWindowsSafeNames(t *testing.T) {
	for _, bad := range []string{"CON", "con.txt", "nul", "COM1.log", "lpt9", "a:b", "x?", "trailing.", "trailing ", "tab\tname"} {
		if windowsSafe(bad) {
			t.Errorf("windowsSafe(%q) = true", bad)
		}
	}
	for _, good := range []string{"console.txt", "com10", "a.b.c", "photo.jpg", "COMMIT", "nullable"} {
		if !windowsSafe(good) {
			t.Errorf("windowsSafe(%q) = false", good)
		}
	}
}

func TestListPrefixCannotEscape(t *testing.T) {
	s := newStore(t)
	put(t, s, "inside.txt", "x")
	// A second bucket next to "test" must never show up.
	if err := s.CreateBucket("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("other", "secret.txt", strings.NewReader("s"), PutOptions{Size: 1}); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"../", "../other/", "a/../../other/", "..", "/etc/"} {
		res, err := s.ListObjects("test", ListOptions{Prefix: prefix, MaxKeys: 100})
		if err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
		if len(res.Objects) > 0 || len(res.CommonPrefixes) > 0 {
			t.Fatalf("prefix %q listed %v %v", prefix, keysOf(res), res.CommonPrefixes)
		}
	}
}

func FuzzKeyPaths(f *testing.F) {
	for _, k := range []string{"a", "a/b", "../x", "dir/", "a/../../b", "%2e%2e/x", "a\\..\\b"} {
		f.Add(k)
	}
	s := &Store{root: "/data"}
	f.Fuzz(func(t *testing.T, key string) {
		if ValidateKey(key) != nil {
			return
		}
		base := s.bucketDir("b") + string(filepath.Separator)
		if p := s.dataPath("b", key); !strings.HasPrefix(p+string(filepath.Separator), base) {
			t.Fatalf("key %q escapes bucket: %s", key, p)
		}
		mbase := s.bucketMetaDir("b") + string(filepath.Separator)
		if p := s.metaPath("b", key); !strings.HasPrefix(p, mbase) {
			t.Fatalf("key %q meta escapes: %s", key, p)
		}
	})
}

func TestPutGetDelete(t *testing.T) {
	s := newStore(t)
	res := put(t, s, "docs/hello.txt", "hello world")
	sum := md5.Sum([]byte("hello world"))
	if res.Info.ETag != hex.EncodeToString(sum[:]) || res.Replaced {
		t.Fatalf("bad put result %+v", res)
	}
	if res.Info.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("content type %q", res.Info.ContentType)
	}
	f, info, err := s.GetObject("test", "docs/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello world" || info.Size != 11 {
		t.Fatalf("got %q size %d", b, info.Size)
	}
	if r2 := put(t, s, "docs/hello.txt", "bye"); !r2.Replaced || r2.OldSize != 11 {
		t.Fatalf("overwrite result %+v", r2)
	}
	size, existed, err := s.DeleteObject("test", "docs/hello.txt")
	if err != nil || !existed || size != 3 {
		t.Fatalf("delete: %d %v %v", size, existed, err)
	}
	if _, err := s.StatObject("test", "docs/hello.txt"); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("expected NoSuchKey, got %v", err)
	}
	// Empty parent folders are pruned.
	if _, err := os.Stat(filepath.Join(s.bucketDir("test"), "docs")); !os.IsNotExist(err) {
		t.Fatalf("docs/ should be pruned, stat err %v", err)
	}
}

func TestPutLimits(t *testing.T) {
	s := newStore(t)
	if _, err := s.PutObject("test", "big", strings.NewReader("12345"), PutOptions{Size: 5, MaxSize: 4}); !errors.Is(err, ErrEntityTooLarge) {
		t.Fatalf("expected too large, got %v", err)
	}
	if _, err := s.PutObject("test", "short", strings.NewReader("12"), PutOptions{Size: 5}); !errors.Is(err, ErrIncompleteBody) {
		t.Fatalf("expected incomplete, got %v", err)
	}
	if _, err := s.PutObject("test", "md5", strings.NewReader("abc"), PutOptions{Size: 3, ContentMD5: "AAAAAAAAAAAAAAAAAAAAAA=="}); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("expected bad digest, got %v", err)
	}
	// Failed writes leave nothing behind.
	for _, k := range []string{"big", "short", "md5"} {
		if _, err := s.StatObject("test", k); !errors.Is(err, ErrNoSuchKey) {
			t.Errorf("%s exists after failed put", k)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(s.root, ".tmp")); len(entries) != 0 {
		t.Errorf(".tmp not cleaned: %d entries", len(entries))
	}
}

func TestPathConflict(t *testing.T) {
	s := newStore(t)
	put(t, s, "a", "file")
	if _, err := s.PutObject("test", "a/b", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	put(t, s, "d/e", "x")
	if _, err := s.PutObject("test", "d", strings.NewReader("x"), PutOptions{Size: 1}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func keysOf(r ListResult) []string {
	var out []string
	for _, o := range r.Objects {
		out = append(out, o.Key)
	}
	return out
}

func TestListOrderAndDelimiter(t *testing.T) {
	s := newStore(t)
	keys := []string{"b", "a/b", "a-c", "a/c/d", "a/c/e", "a0", "z/", "z/1"}
	for _, k := range keys {
		body := "x"
		if strings.HasSuffix(k, "/") {
			body = ""
		}
		put(t, s, k, body)
	}
	res, err := s.ListObjects("test", ListOptions{MaxKeys: 1000, FetchMeta: true})
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(keys)
	slices.Sort(want)
	if got := keysOf(res); !slices.Equal(got, want) {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}

	res, _ = s.ListObjects("test", ListOptions{Delimiter: "/", MaxKeys: 1000})
	if got := keysOf(res); !slices.Equal(got, []string{"a-c", "a0", "b"}) {
		t.Fatalf("delimiter objects %v", got)
	}
	if !slices.Equal(res.CommonPrefixes, []string{"a/", "z/"}) {
		t.Fatalf("prefixes %v", res.CommonPrefixes)
	}

	res, _ = s.ListObjects("test", ListOptions{Prefix: "a/", Delimiter: "/", MaxKeys: 1000})
	if got := keysOf(res); !slices.Equal(got, []string{"a/b"}) || !slices.Equal(res.CommonPrefixes, []string{"a/c/"}) {
		t.Fatalf("prefix a/: %v %v", got, res.CommonPrefixes)
	}

	// The folder marker itself is listed under its own prefix.
	res, _ = s.ListObjects("test", ListOptions{Prefix: "z/", Delimiter: "/", MaxKeys: 1000})
	if got := keysOf(res); !slices.Equal(got, []string{"z/", "z/1"}) {
		t.Fatalf("folder marker listing %v", got)
	}
}

func TestListPagination(t *testing.T) {
	s := newStore(t)
	var all []string
	for _, k := range []string{"p/1", "p/2", "q/1", "r", "s/t/u", "s/v"} {
		put(t, s, k, "x")
		all = append(all, k)
	}
	for _, delim := range []string{"", "/"} {
		var got []string
		marker := ""
		for i := 0; i < 20; i++ {
			res, err := s.ListObjects("test", ListOptions{Delimiter: delim, Marker: marker, MaxKeys: 2})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, keysOf(res)...)
			got = append(got, res.CommonPrefixes...)
			if !res.IsTruncated {
				break
			}
			marker = res.NextMarker
		}
		want := all
		if delim == "/" {
			want = []string{"p/", "q/", "r", "s/"}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("delim %q pages: got %v want %v", delim, got, want)
		}
	}
}

func TestExternalFileGetsMetadata(t *testing.T) {
	s := newStore(t)
	p := filepath.Join(s.bucketDir("test"), "rsynced.txt")
	if err := os.WriteFile(p, []byte("from rsync"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := s.StatObject("test", "rsynced.txt")
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("from rsync"))
	if info.ETag != hex.EncodeToString(sum[:]) || info.Size != 10 {
		t.Fatalf("synthesized meta %+v", info)
	}
}

func TestMultipart(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateMultipartUpload("test", "big.bin", PutOptions{ContentType: "application/x-test"})
	if err != nil {
		t.Fatal(err)
	}
	p1 := bytes.Repeat([]byte("a"), MinPartSize)
	p2 := []byte("tail")
	part1, err := s.UploadPart("test", "big.bin", id, 1, bytes.NewReader(p1), int64(len(p1)), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	part2, err := s.UploadPart("test", "big.bin", id, 2, bytes.NewReader(p2), int64(len(p2)), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteMultipartUpload("test", "big.bin", id, []CompletePart{{2, part2.ETag}, {1, part1.ETag}}, nil); !errors.Is(err, ErrInvalidPartOrder) {
		t.Fatalf("expected order error, got %v", err)
	}
	if _, err := s.CompleteMultipartUpload("test", "big.bin", id, []CompletePart{{1, "bad"}}, nil); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("expected invalid part, got %v", err)
	}
	res, err := s.CompleteMultipartUpload("test", "big.bin", id, []CompletePart{{1, `"` + part1.ETag + `"`}, {2, part2.ETag}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// ETag = md5(md5(p1) || md5(p2)) + "-2"
	m1, m2 := md5.Sum(p1), md5.Sum(p2)
	all := md5.Sum(append(m1[:], m2[:]...))
	if want := hex.EncodeToString(all[:]) + "-2"; res.Info.ETag != want {
		t.Fatalf("etag %s want %s", res.Info.ETag, want)
	}
	if res.Info.Size != int64(len(p1)+len(p2)) || res.Info.ContentType != "application/x-test" {
		t.Fatalf("info %+v", res.Info)
	}
	if _, err := os.Stat(s.uploadDir(id)); !os.IsNotExist(err) {
		t.Fatal("upload dir not removed")
	}
}

func TestBuckets(t *testing.T) {
	s := newStore(t)
	if err := s.CreateBucket("test"); !errors.Is(err, ErrBucketExists) {
		t.Fatalf("expected exists, got %v", err)
	}
	for _, bad := range []string{"UPPER", "ab", "img", "a_b", "192.168.1.1"} {
		if err := s.CreateBucket(bad); !errors.Is(err, ErrInvalidBucketName) {
			t.Errorf("CreateBucket(%q) = %v", bad, err)
		}
	}
	put(t, s, "x", "1")
	if err := s.DeleteBucket("test", false); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("expected not empty, got %v", err)
	}
	if err := s.DeleteBucket("test", true); err != nil {
		t.Fatal(err)
	}
	if s.BucketExists("test") {
		t.Fatal("bucket still exists")
	}
}
