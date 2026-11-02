package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/getsettalk/Tijori/internal/config"
)

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func TestPublicFolders(t *testing.T) {
	e := startServer(t, func(c *config.Config) {
		c.Admin.Password = "admin-password"
		c.Buckets["shop"] = config.BucketConfig{PublicPrefixes: []string{"public/"}}
	})
	put := func(key, body string) {
		t.Helper()
		if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("shop"), Key: aws.String(key), Body: strings.NewReader(body)}); err != nil {
			t.Fatal(err)
		}
	}
	put("public/banner.txt", "for everyone")
	put("public/sub/deep.txt", "deep")
	put("public2/look-alike.txt", "private")
	put("invoices/42.txt", "private")
	var img bytes.Buffer
	_ = jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 64, 48)), nil)
	put("public/photo.jpg", img.String())

	for path, want := range map[string]int{
		"/shop/public/banner.txt":          200,
		"/shop/public/sub/deep.txt":        200,
		"/shop/public2/look-alike.txt":     403, // only the folder, not names that start alike
		"/shop/invoices/42.txt":            403,
		"/shop":                            403, // listing stays private
		"/shop?list-type=2&prefix=public/": 403,
		"/img/shop/public/photo.jpg?w=20":  200,
		"/img/shop/invoices/42.txt?w=20":   403,
	} {
		if resp, body := get(t, e.url+path); resp.StatusCode != want {
			t.Errorf("anonymous GET %s: %d, want %d (%s)", path, resp.StatusCode, want, body)
		}
	}
	// Writing is never public.
	req, _ := http.NewRequest("PUT", e.url+"/shop/public/evil.txt", strings.NewReader("x"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("anonymous PUT into a public folder: %v %d", err, resp.StatusCode)
	}
	// On case-insensitive disks "PUBLIC/" is the same folder, so it must be
	// public too; elsewhere it is a different (missing, private) name.
	resp, _ := get(t, e.url+"/shop/PUBLIC/banner.txt")
	if runtime.GOOS == "linux" && resp.StatusCode != 403 {
		t.Errorf("PUBLIC/ on Linux: %d", resp.StatusCode)
	}
	if (runtime.GOOS == "darwin" || runtime.GOOS == "windows") && resp.StatusCode != 200 {
		t.Errorf("PUBLIC/ on a case-insensitive disk: %d", resp.StatusCode)
	}
	// The object ACL tells SDKs which files are public.
	acl, err := e.s3.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String("shop"), Key: aws.String("public/banner.txt")})
	if err != nil || len(acl.Grants) != 2 {
		t.Fatalf("public file ACL: %v %d grants", err, len(acl.Grants))
	}
	acl, err = e.s3.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String("shop"), Key: aws.String("invoices/42.txt")})
	if err != nil || len(acl.Grants) != 1 {
		t.Fatalf("private file ACL: %v %d grants", err, len(acl.Grants))
	}

	// The admin panel marks public files and sets folders on its own buckets.
	a := newAdminClient(t, e)
	a.login("admin", "admin-password")
	_, out := a.do("GET", "/objects?bucket=shop&prefix=public/", nil, false)
	if files := out["files"].([]any); len(files) == 0 || files[0].(map[string]any)["public"] != true {
		t.Fatalf("public flag missing: %v", out)
	}
	if st, out := a.do("POST", "/buckets", map[string]any{"name": "media", "public_prefixes": []string{"avatars", "../x"}}, true); st != 400 {
		t.Fatalf("unsafe folder accepted: %d %v", st, out)
	}
	if st, out := a.do("POST", "/buckets", map[string]any{"name": "media", "public_prefixes": []string{"/avatars"}}, true); st != 201 {
		t.Fatalf("create with public folders: %d %v", st, out)
	}
	_, list := a.do("GET", "/overview", nil, false)
	for _, b := range list["buckets"].([]any) {
		if bv := b.(map[string]any); bv["name"] == "media" && (len(bv["public_prefixes"].([]any)) != 1 || bv["public_prefixes"].([]any)[0] != "avatars/") {
			t.Fatalf("stored folders: %v", bv)
		}
	}
	if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("media"), Key: aws.String("avatars/1.txt"), Body: strings.NewReader("hi")}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(t, e.url+"/media/avatars/1.txt"); resp.StatusCode != 200 {
		t.Fatalf("folder set in the admin panel: %d", resp.StatusCode)
	}
}

func TestRateLimits(t *testing.T) {
	var logs syncBuffer
	e := startServerLog(t, func(c *config.Config) {
		c.Metrics = true
		c.Admin.Password = "admin-password"
		c.Limits = config.LimitsConfig{KeyRPS: 3, AnonymousRPS: 2}
		c.Buckets["pics"] = config.BucketConfig{PublicRead: true}
	}, &logs)
	if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("pics"), Key: aws.String("a.txt"), Body: strings.NewReader("hello")}); err != nil {
		t.Fatal(err)
	}
	// A key gets a burst of twice its rate, then SlowDown.
	var throttled error
	for i := 0; i < 50 && throttled == nil; i++ {
		var out *s3.GetObjectOutput
		if out, throttled = e.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("pics"), Key: aws.String("a.txt")}); throttled == nil {
			out.Body.Close()
		}
	}
	if errCode(throttled) != "SlowDown" {
		t.Fatalf("key limit: %v", throttled)
	}
	// A key's own limit, set in the admin panel, can lift it.
	a := newAdminClient(t, e)
	a.login("admin", "admin-password")
	if st, out := a.do("PUT", "/keys/"+e.key.AccessKey, map[string]any{"rate_limit": -2}, true); st != 400 {
		t.Fatalf("bad limit accepted: %d %v", st, out)
	}
	if st, out := a.do("PUT", "/keys/"+e.key.AccessKey, map[string]any{"rate_limit": -1}, true); st != 200 {
		t.Fatalf("set key limit: %d %v", st, out)
	}
	for i := 0; i < 30; i++ {
		if _, err := e.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("pics"), Key: aws.String("a.txt")}); err != nil {
			t.Fatalf("exempt key limited: %v", err)
		}
	}
	// Anonymous clients: S3 says SlowDown with Retry-After, /img/ says 429.
	// (Loops stop at the first refusal, so slow test machines still work.)
	var last *http.Response
	for i := 0; i < 50 && (last == nil || last.StatusCode == 200); i++ {
		last, _ = get(t, e.url+"/pics/a.txt")
	}
	if last.StatusCode != 503 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("anonymous limit: %d retry-after=%q", last.StatusCode, last.Header.Get("Retry-After"))
	}
	for i := 0; i < 50 && last.StatusCode != 429; i++ {
		last, _ = get(t, e.url+"/img/pics/a.txt?w=10")
	}
	if last.StatusCode != 429 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("/img/ limit: %d", last.StatusCode)
	}
	// Health checks and the admin panel are never limited.
	for i := 0; i < 10; i++ {
		if resp, _ := get(t, e.url+"/health"); resp.StatusCode != 200 {
			t.Fatal("health check limited")
		}
	}
	if _, body := get(t, e.url+"/metrics"); !strings.Contains(string(body), "tijori_s3_throttled_total") || strings.Contains(string(body), "tijori_s3_throttled_total 0\n") {
		t.Fatalf("throttle metric missing or zero")
	}

	// The access log says which bucket, file and key, never the secret.
	found := false
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) != nil || line["msg"] != "request" {
			continue
		}
		if line["bucket"] == "pics" && line["key"] == "a.txt" && line["access_key"] == e.key.AccessKey && line["method"] == "PUT" && line["bytes_in"] == 5.0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no access log line with bucket, key and access key:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `"access_key":"anonymous"`) {
		t.Fatal("anonymous requests are not marked in the log")
	}
	if strings.Contains(logs.String(), e.key.SecretKey) {
		t.Fatal("secret key in the log")
	}
}

// syncBuffer is a bytes.Buffer safe for the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
