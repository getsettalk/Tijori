package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/getsettalk/Tijori/internal/config"
)

type adminClient struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func newAdminClient(t *testing.T, e *testEnv) *adminClient {
	jar, _ := cookiejar.New(nil)
	return &adminClient{t: t, base: e.url + "/_admin/api", c: &http.Client{Jar: jar}}
}

func (a *adminClient) do(method, path string, body any, csrf bool) (int, map[string]any) {
	var r io.Reader
	if b, ok := body.([]byte); ok {
		r = bytes.NewReader(b)
	} else if body != nil {
		j, _ := json.Marshal(body)
		r = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, a.base+path, r)
	if csrf {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (a *adminClient) login(user, pw string) int {
	st, out := a.do("POST", "/login", map[string]string{"username": user, "password": pw}, false)
	if st == 200 {
		a.csrf, _ = out["csrf"].(string)
	}
	return st
}

func TestAdminPanel(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.Admin.Password = "correct-horse" })
	a := newAdminClient(t, e)

	// Static page is served with a strict CSP.
	resp, err := http.Get(e.url + "/_admin/")
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("index: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()

	if st, _ := a.do("GET", "/overview", nil, false); st != 401 {
		t.Fatalf("unauthenticated overview: %d", st)
	}
	if st := a.login("admin", "wrong-password"); st != 401 {
		t.Fatalf("wrong password: %d", st)
	}
	if st := a.login("admin", "correct-horse"); st != 200 {
		t.Fatalf("login: %d", st)
	}
	if st, out := a.do("GET", "/overview", nil, false); st != 200 || out["version"] == nil {
		t.Fatalf("overview: %d %v", st, out)
	}

	// Mutations need the CSRF token.
	if st, _ := a.do("POST", "/buckets", map[string]any{"name": "docs"}, false); st != 403 {
		t.Fatalf("missing CSRF accepted: %d", st)
	}
	if st, out := a.do("POST", "/buckets", map[string]any{"name": "docs", "max_size": "1MB"}, true); st != 201 {
		t.Fatalf("create bucket: %d %v", st, out)
	}

	// Upload, list, download, delete.
	if st, out := a.do("PUT", "/object?bucket=docs&key=notes/readme.txt", []byte("hello admin"), true); st != 200 {
		t.Fatalf("upload: %d %v", st, out)
	}
	if st, out := a.do("PUT", "/object?bucket=docs&key=big.bin", make([]byte, 2<<20), true); st != 403 || !strings.Contains(out["error"].(string), "full") {
		t.Fatalf("quota via admin: %d %v", st, out)
	}
	st, out := a.do("GET", "/objects?bucket=docs&prefix=", nil, false)
	if st != 200 || len(out["folders"].([]any)) != 1 {
		t.Fatalf("list: %d %v", st, out)
	}
	req, _ := http.NewRequest("GET", a.base+"/file?bucket=docs&key=notes/readme.txt&inline=1", nil)
	resp, err = a.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello admin" || resp.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("download: %q csp=%q", body, resp.Header.Get("Content-Security-Policy"))
	}
	if st, out := a.do("POST", "/presign", map[string]any{"bucket": "docs", "key": "notes/readme.txt", "expires": 600}, true); st != 200 {
		t.Fatalf("presign: %d %v", st, out)
	} else if r2, err := http.Get(out["url"].(string)); err != nil || r2.StatusCode != 200 {
		t.Fatalf("presigned link from admin does not work: %v", err)
	}
	if st, _ := a.do("DELETE", "/object?bucket=docs&key=notes/", nil, true); st != 200 {
		t.Fatalf("folder delete: %d", st)
	}
	if u := e.srv.Quota.BucketUsage("docs"); u.Objects != 0 {
		t.Fatalf("usage after delete: %+v", u)
	}

	// Keys: create returns the secret once; list never does.
	st, out = a.do("POST", "/keys", map[string]any{"name": "app", "buckets": []string{"docs"}, "permission": "read"}, true)
	if st != 201 || out["secret_key"] == nil {
		t.Fatalf("create key: %d %v", st, out)
	}
	req, _ = http.NewRequest("GET", a.base+"/keys", nil)
	resp, _ = a.c.Do(req)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), out["secret_key"].(string)) {
		t.Fatal("key list leaks secrets")
	}

	// Logout ends the session.
	if st, _ := a.do("POST", "/logout", nil, true); st != 200 {
		t.Fatalf("logout: %d", st)
	}
	if st, _ := a.do("GET", "/overview", nil, false); st != 401 {
		t.Fatalf("after logout: %d", st)
	}
}

func TestAdminReadOnlyAndRateLimit(t *testing.T) {
	e := startServer(t, nil)
	if err := EnsureAdminPassword(e.srv.DB, "viewer", "viewer-pass"); err != nil {
		t.Fatal(err)
	}
	u, _ := e.srv.DB.User("viewer")
	u.Role = "readonly"
	_ = e.srv.DB.PutUser(u)

	a := newAdminClient(t, e)
	if st := a.login("viewer", "viewer-pass"); st != 200 {
		t.Fatalf("login: %d", st)
	}
	if st, _ := a.do("POST", "/buckets", map[string]any{"name": "nope"}, true); st != 403 {
		t.Fatalf("read-only user created a bucket: %d", st)
	}
	if st, _ := a.do("GET", "/buckets", nil, false); st != 200 {
		t.Fatalf("read-only list: %d", st)
	}

	b := newAdminClient(t, e)
	for i := 0; i < 5; i++ {
		b.login("viewer", "bad")
	}
	if st := b.login("viewer", "viewer-pass"); st != 429 {
		t.Fatalf("expected rate limit, got %d", st)
	}
}

// Behind a reverse proxy every request comes from the proxy's address. One
// attacker's failed logins must not lock out everybody else.
func TestAdminRateLimitBehindProxy(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.Admin.Password = "correct-horse" })
	login := func(xff, pw string) int {
		body := strings.NewReader(`{"username":"admin","password":"` + pw + `"}`)
		req, _ := http.NewRequest("POST", e.url+"/_admin/api/login", body)
		req.Header.Set("X-Forwarded-For", xff)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 5; i++ {
		login("198.51.100.66", "wrong")
	}
	if st := login("198.51.100.66", "correct-horse"); st != 429 {
		t.Fatalf("attacker IP should be blocked, got %d", st)
	}
	if st := login("198.51.100.10", "correct-horse"); st != 200 {
		t.Fatalf("a different client behind the same proxy was blocked: %d", st)
	}
}
