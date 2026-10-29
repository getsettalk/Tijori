package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true},
		{"v1.2.0", "1.1.9", true},
		{"1.10.0", "1.9.0", true},
		{"2.0.0", "1.99.99", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1", false},
		{"1.0.1", "1.0.1+dirty", false},
		{"1.0.2", "1.0.2-0.20261029120000-abcdef123456", false},
		{"1.0.1", "dev", false},
		{"dev", "1.0.0", false},
		{"1.0", "0.9.0", false},
		{"1.0.x", "0.9.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func fakeGitHub(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Tijori/") || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected request headers: %v", r.Header)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestChecker(t *testing.T) {
	ctx := context.Background()
	release := `{"tag_name":"v1.2.0","html_url":"https://github.com/getsettalk/Tijori/releases/tag/v1.2.0"}`

	c := &Checker{Current: "1.0.1", URL: fakeGitHub(t, 200, release)}
	if s := c.Status(); s.Latest != "" || s.Available || s.CheckedAt != nil || !s.Enabled {
		t.Fatalf("before the first check: %+v", s)
	}
	c.Check(ctx)
	s := c.Status()
	if !s.Available || s.Latest != "1.2.0" || s.Current != "1.0.1" || s.CheckedAt == nil ||
		s.URL != "https://github.com/getsettalk/Tijori/releases/tag/v1.2.0" {
		t.Fatalf("older server: %+v", s)
	}

	c = &Checker{Current: "1.2.0", URL: fakeGitHub(t, 200, release)}
	c.Check(ctx)
	if s := c.Status(); s.Available || s.Latest != "1.2.0" {
		t.Fatalf("current server: %+v", s)
	}

	// Errors, drafts and odd tags leave the status empty.
	for name, srv := range map[string]string{
		"rate limited": fakeGitHub(t, 403, `{"message":"API rate limit exceeded"}`),
		"not json":     fakeGitHub(t, 200, `<html>`),
		"bad tag":      fakeGitHub(t, 200, `{"tag_name":"<script>","html_url":"https://github.com/getsettalk/Tijori/releases/x"}`),
		"prerelease":   fakeGitHub(t, 200, `{"tag_name":"v9.0.0-rc1","prerelease":true}`),
		"offline":      "http://127.0.0.1:1",
	} {
		c := &Checker{Current: "1.0.1", URL: srv}
		c.Check(ctx)
		if s := c.Status(); s.Latest != "" || s.Available {
			t.Errorf("%s: %+v", name, s)
		}
	}

	// A link outside the release page is replaced, so a tampered response
	// cannot put another address into the admin panel.
	c = &Checker{Current: "1.0.1", URL: fakeGitHub(t, 200, `{"tag_name":"v1.3.0","html_url":"javascript:alert(1)"}`)}
	c.Check(ctx)
	if s := c.Status(); s.URL != "https://github.com/getsettalk/Tijori/releases/tag/v1.3.0" {
		t.Fatalf("unsafe link kept: %q", s.URL)
	}
}

func TestRunStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c := &Checker{Current: "1.0.1", URL: fakeGitHub(t, 200, `{"tag_name":"v1.0.1"}`)}
	go func() { c.Run(ctx); close(done) }()
	cancel()
	<-done
}
