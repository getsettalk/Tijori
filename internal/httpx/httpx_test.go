package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:1234", "", "203.0.113.9"},                     // direct client
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},              // public peer: XFF ignored (spoof)
		{"10.0.5.3:5555", "198.51.100.7", "198.51.100.7"},           // behind Coolify/Traefik
		{"127.0.0.1:5555", "6.6.6.6, 198.51.100.7", "198.51.100.7"}, // client tried to spoof; proxy appended real IP
		{"10.0.5.3:5555", "198.51.100.7, 10.0.0.2", "198.51.100.7"}, // two proxies
		{"10.0.5.3:5555", "", "10.0.5.3"},                           // private, no header
		{"10.0.5.3:5555", "garbage", "10.0.5.3"},                    // bad header
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r); got != c.want {
			t.Errorf("remote %s xff %q: got %s want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestCORS(t *testing.T) {
	c := CORS{Origins: []string{"https://app.example.com"}}
	pre := httptest.NewRequest(http.MethodOptions, "/photos/a.jpg", nil)
	pre.Header.Set("Origin", "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", "PUT")
	pre.Header.Set("Access-Control-Request-Headers", "content-type, x-amz-date, bad header!")
	w := httptest.NewRecorder()
	if !c.Handle(w, pre) || w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "content-type, x-amz-date" {
		t.Fatalf("allow headers %q", got)
	}
	evil := pre.Clone(pre.Context())
	evil.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	if !c.Handle(w, evil) || w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("other origin allowed: %d %v", w.Code, w.Header())
	}
	off := CORS{}
	w = httptest.NewRecorder()
	if !off.Handle(w, pre) || w.Code != 403 {
		t.Fatalf("CORS off must refuse preflight: %d", w.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/photos/a.jpg", nil)
	get.Header.Set("Origin", "https://app.example.com")
	w = httptest.NewRecorder()
	if c.Handle(w, get) || w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("simple request should get CORS headers and continue")
	}
	star := CORS{Origins: []string{"*"}}
	w = httptest.NewRecorder()
	star.Handle(w, get)
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("wildcard")
	}
}
