package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/crypto/bcrypt"

	"github.com/getsettalk/Tijori/internal/backup"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

// download asks for a one-time link and fetches the full backup.
func download(t *testing.T, a *adminClient, url, password string) []byte {
	t.Helper()
	st, out := a.do("POST", "/export", map[string]string{"password": password}, true)
	if st != 200 {
		t.Fatalf("export link: %d %v", st, out)
	}
	link, _ := out["url"].(string)
	resp, err := a.c.Get(url + link)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") ||
		resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %v", resp.StatusCode, resp.Header)
	}
	// The link works once.
	again, err := a.c.Get(url + link)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != 403 {
		t.Fatalf("download link used twice: %d", again.StatusCode)
	}
	return body
}

// restore uploads a backup in parts the way the admin panel does, including
// a part sent twice, and waits for the result.
func restore(t *testing.T, a *adminClient, data []byte, password string, replace bool) map[string]any {
	t.Helper()
	st, out := a.do("POST", "/restore", map[string]any{"size": len(data), "password": password}, true)
	if st != 201 {
		t.Fatalf("restore start: %d %v", st, out)
	}
	id := out["id"].(string)
	part := len(data)/3 + 1
	send := func(off, end int) {
		t.Helper()
		path := fmt.Sprintf("/restore/part?id=%s&offset=%d", id, off)
		if st, out := a.do("PUT", path, data[off:end], true); st != 200 || int(out["received"].(float64)) != end {
			t.Fatalf("part %d-%d: %d %v", off, end, st, out)
		}
	}
	first := min(part, len(data))
	send(0, first)
	// A retry after a lost answer sends the same bytes again, and the admin
	// panel retries a cut part at half its size: both replace what came
	// before at that offset.
	send(0, first)
	send(0, first/2)
	if st, _ := a.do("PUT", fmt.Sprintf("/restore/part?id=%s&offset=%d", id, first/2+1), []byte("x"), true); st != 409 {
		t.Fatalf("part with a gap accepted: %d", st)
	}
	for off := first / 2; off < len(data); off += part {
		send(off, min(off+part, len(data)))
	}
	if st, out := a.do("POST", "/restore/finish", map[string]any{"id": id, "replace": replace}, true); st != 200 {
		t.Fatalf("restore finish: %d %v", st, out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, out := a.do("GET", "/restore", nil, false)
		if s := out["state"]; s == "done" || s == "failed" {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("restore did not finish: %v", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func getText(t *testing.T, c *s3.Client, bucket, key string) (string, *s3.GetObjectOutput) {
	t.Helper()
	out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get %s/%s: %v", bucket, key, err)
	}
	b, _ := io.ReadAll(out.Body)
	out.Body.Close()
	return string(b), out
}

func TestAdminDownloadAndRestore(t *testing.T) {
	src := startServer(t, func(c *config.Config) { c.Admin.Password = "source-password" })
	seed(t, src)
	if err := src.srv.Store.SetBucketSettings("photos", storage.BucketSettings{PublicRead: true, MaxSize: 10 << 20}); err != nil {
		t.Fatal(err)
	}
	a := newAdminClient(t, src)
	if st := a.login("admin", "source-password"); st != 200 {
		t.Fatalf("login: %d", st)
	}

	// The file holds every secret, so the password is asked again.
	if st, _ := a.do("POST", "/export", map[string]string{"password": "wrong"}, true); st != 403 {
		t.Fatalf("export with a wrong password: %d", st)
	}
	if st, _ := a.do("GET", "/export?token=guess", nil, false); st != 403 {
		t.Fatalf("export without a link: %d", st)
	}
	archive := download(t, a, src.url, "source-password")

	// Same format as `tijori export`: the command line can import it.
	dir := t.TempDir()
	if _, err := backup.Import(bytes.NewReader(archive), dir, "", false); err != nil {
		t.Fatalf("tijori import of the download: %v", err)
	}
	want := checksums(t, src.srv.Cfg.DataDir)
	if got := checksums(t, dir); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("download differs from the server:\n got %v\nwant %v", got, want)
	}

	// Restore into another, running server.
	dst := startServer(t, func(c *config.Config) { c.Admin.Password = "target-password" })
	b := newAdminClient(t, dst)
	if st := b.login("admin", "target-password"); st != 200 {
		t.Fatalf("login: %d", st)
	}
	if st, _ := b.do("POST", "/restore", map[string]any{"size": len(archive), "password": "source-password"}, true); st != 403 {
		t.Fatalf("restore with the other server's password: %d", st)
	}
	res := restore(t, b, archive, "target-password", true)
	if res["state"] != "done" {
		t.Fatalf("restore: %v", res)
	}
	r := res["result"].(map[string]any)
	if r["files"].(float64) != 3 || len(r["buckets_created"].([]any)) != 2 || r["keys_added"].(float64) != 1 || r["users_added"].(float64) != 0 {
		t.Fatalf("restore result: %v", r)
	}
	if got := checksums(t, dst.srv.Cfg.DataDir); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("restored files differ:\n got %v\nwant %v", got, want)
	}
	// The source's key works on the new server, with the original headers.
	old := newClient(dst.url, src.key.AccessKey, src.key.SecretKey)
	if body, out := getText(t, old, "photos", "a.jpg"); body != "aaa" || aws.ToString(out.ContentType) != "application/x-test" || out.Metadata["k"] != "v" {
		t.Fatalf("restored object: %q %v %v", body, aws.ToString(out.ContentType), out.Metadata)
	}
	if _, err := old.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("docs"), Key: aws.String("folder/")}); err != nil {
		t.Fatalf("folder marker: %v", err)
	}
	if s := dst.srv.Quota.Settings("photos"); !s.PublicRead || s.MaxSize != 10<<20 {
		t.Fatalf("bucket settings: %+v", s)
	}
	if u := dst.srv.Quota.BucketUsage("photos"); u.Objects != 2 {
		t.Fatalf("usage not recounted: %+v", u)
	}
	// The existing admin keeps their password.
	if st := newAdminClient(t, dst).login("admin", "source-password"); st != 401 {
		t.Fatalf("restore changed the admin password: %d", st)
	}

	// Keep mode leaves changed files alone; replace mode puts them back.
	if _, err := dst.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("a.jpg"), Body: strings.NewReader("changed")}); err != nil {
		t.Fatal(err)
	}
	res = restore(t, b, archive, "target-password", false)
	if r := res["result"].(map[string]any); res["state"] != "done" || r["skipped"].(float64) != 3 || r["files"].(float64) != 0 {
		t.Fatalf("keep mode: %v", res)
	}
	if body, _ := getText(t, dst.s3, "photos", "a.jpg"); body != "changed" {
		t.Fatalf("keep mode replaced a file: %q", body)
	}
	res = restore(t, b, archive, "target-password", true)
	if body, _ := getText(t, dst.s3, "photos", "a.jpg"); res["state"] != "done" || body != "aaa" {
		t.Fatalf("replace mode: %v %q", res, body)
	}

	// A cut-off download is refused before anything changes.
	if _, err := dst.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("a.jpg"), Body: strings.NewReader("changed")}); err != nil {
		t.Fatal(err)
	}
	res = restore(t, b, archive[:len(archive)*2/3], "target-password", true)
	if res["state"] != "failed" || !strings.Contains(res["error"].(string), "backup file") {
		t.Fatalf("cut-off file: %v", res)
	}
	if body, _ := getText(t, dst.s3, "photos", "a.jpg"); body != "changed" {
		t.Fatalf("a refused file changed data: %q", body)
	}
	// Something that is not a backup at all.
	res = restore(t, b, []byte("hello, this is not a backup"), "target-password", true)
	if res["state"] != "failed" || !strings.Contains(res["error"].(string), "not a Tijori backup") {
		t.Fatalf("not a backup: %v", res)
	}
}

func TestBackupFileNeedsAdmin(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.Admin.Password = "admin-password" })
	hash, _ := bcrypt.GenerateFromPassword([]byte("viewer-password"), bcrypt.MinCost)
	if err := e.srv.DB.PutUser(state.AdminUser{Username: "viewer", PasswordHash: string(hash), Role: "readonly"}); err != nil {
		t.Fatal(err)
	}
	v := newAdminClient(t, e)
	if st := v.login("viewer", "viewer-password"); st != 200 {
		t.Fatalf("login: %d", st)
	}
	if st, _ := v.do("POST", "/export", map[string]string{"password": "viewer-password"}, true); st != 403 {
		t.Fatalf("read-only user got a download link: %d", st)
	}
	if st, _ := v.do("POST", "/restore", map[string]any{"size": 10, "password": "viewer-password"}, true); st != 403 {
		t.Fatalf("read-only user started a restore: %d", st)
	}
	// Only the admin who started a restore upload can continue it.
	hash, _ = bcrypt.GenerateFromPassword([]byte("other-password"), bcrypt.MinCost)
	if err := e.srv.DB.PutUser(state.AdminUser{Username: "other", PasswordHash: string(hash), Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	a, o := newAdminClient(t, e), newAdminClient(t, e)
	a.login("admin", "admin-password")
	o.login("other", "other-password")
	st, out := a.do("POST", "/restore", map[string]any{"size": 4, "password": "admin-password"}, true)
	if st != 201 {
		t.Fatalf("restore start: %d %v", st, out)
	}
	id := out["id"].(string)
	if st, _ := o.do("PUT", "/restore/part?id="+id+"&offset=0", []byte("evil"), true); st != 404 {
		t.Fatalf("another admin wrote into the upload: %d", st)
	}
	if st, _ := a.do("PUT", "/restore/part?id="+id+"&offset=0", []byte("good"), true); st != 200 {
		t.Fatalf("owner's part: %d", st)
	}
	if st, _ := o.do("POST", "/restore/finish", map[string]any{"id": id, "replace": true}, true); st != 409 {
		t.Fatalf("another admin finished the upload: %d", st)
	}

	// Without a session nothing is reachable.
	resp, err := http.Get(e.url + "/_admin/api/export?token=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous export: %d", resp.StatusCode)
	}
}
