// Package httpx has small HTTP helpers shared by the S3 API, the image
// route and the admin panel: the real client IP behind a reverse proxy, and
// CORS for browser apps.
package httpx

import (
	"net"
	"net/http"
	"slices"
	"strings"
)

// TrustForwarded enables reading X-Forwarded-For from private and loopback
// peers (the trust_proxy_headers setting). Turn it off when Tijori is not
// behind a reverse proxy and clients on the local network could send the
// header themselves.
var TrustForwarded = true

// ClientIP returns the address of the client. When the direct peer is a
// loopback or private address (a reverse proxy such as Caddy, Nginx or
// Coolify's Traefik on the same host or network), the right-most public
// address in X-Forwarded-For is used instead. The right-most entry is the
// one added by the proxy itself, so clients cannot spoof it by sending
// their own header.
func ClientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	ip := net.ParseIP(peer)
	if !TrustForwarded || ip == nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		return peer
	}
	xff := r.Header.Values("X-Forwarded-For")
	var hops []string
	for _, v := range xff {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	// Walk from the proxy towards the client and take the first address
	// that is not another internal proxy.
	for i := len(hops) - 1; i >= 0; i-- {
		h := net.ParseIP(hops[i])
		if h == nil {
			break
		}
		if !(h.IsLoopback() || h.IsPrivate()) {
			return h.String()
		}
		if i == 0 {
			return h.String()
		}
	}
	if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-Ip"))); real != nil && len(hops) == 0 {
		return real.String()
	}
	return peer
}

// CORS answers browser cross-origin requests for the configured origins.
// An empty list turns CORS off; "*" allows every origin. Requests are still
// authenticated by signatures (or are public reads), so allowing an origin
// never grants access by itself; it only lets that site's JavaScript read
// the responses.
type CORS struct {
	Origins []string
}

// Enabled reports whether any origin is allowed.
func (c CORS) Enabled() bool { return len(c.Origins) > 0 }

func (c CORS) allowed(origin string) (string, bool) {
	if origin == "" || !c.Enabled() {
		return "", false
	}
	if slices.Contains(c.Origins, "*") {
		return "*", true
	}
	for _, o := range c.Origins {
		if strings.EqualFold(strings.TrimSuffix(o, "/"), origin) {
			return origin, true
		}
	}
	return "", false
}

// Handle adds CORS headers. For a preflight (OPTIONS) request it writes the
// whole response and returns true; the caller must then stop.
func (c CORS) Handle(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	allow, ok := c.allowed(origin)
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	if !ok {
		if preflight {
			// S3 answers 403 when no CORS rule matches.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("CORS is not enabled for this origin. Set cors_origins in the Tijori config.\n"))
			return true
		}
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", allow)
	h.Add("Vary", "Origin")
	if !preflight {
		h.Set("Access-Control-Expose-Headers", "ETag, Content-Length, Content-Range, Content-Type, Last-Modified, x-amz-request-id, x-amz-version-id, X-Tijori-Cache")
		return false
	}
	h.Add("Vary", "Access-Control-Request-Method")
	h.Add("Vary", "Access-Control-Request-Headers")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, POST, DELETE")
	if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
		h.Set("Access-Control-Allow-Headers", sanitizeHeaderList(req))
	}
	h.Set("Access-Control-Max-Age", "3600")
	w.WriteHeader(http.StatusNoContent)
	return true
}

// sanitizeHeaderList keeps only valid header-name tokens.
func sanitizeHeaderList(v string) string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > 100 {
			continue
		}
		valid := true
		for _, c := range p {
			if !(c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
				valid = false
				break
			}
		}
		if valid {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}
