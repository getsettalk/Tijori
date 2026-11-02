// Package ratelimit limits requests per access key and, for requests
// without a key, per client address. Each one gets a token bucket that
// refills at the allowed rate and holds up to twice as many tokens, so
// short bursts are fine and a steady flood is slowed down.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/getsettalk/Tijori/internal/httpx"
	"github.com/getsettalk/Tijori/internal/state"
)

// Policy holds the limits and the buckets. Its zero value (or nil) limits
// nothing.
type Policy struct {
	KeyRPS       int // default per access key; 0 = no limit
	AnonymousRPS int // per client address for requests without a key

	keys    limiter
	clients limiter
}

// Allow reports whether a request may go ahead. key is nil for anonymous
// requests. When it may not, wait says when to try again.
func (p *Policy) Allow(r *http.Request, key *state.AccessKey) (ok bool, wait time.Duration) {
	if p == nil {
		return true, 0
	}
	if key != nil {
		rate := p.KeyRPS
		switch {
		case key.RateLimit > 0:
			rate = key.RateLimit
		case key.RateLimit < 0: // this key is exempt
			rate = 0
		}
		return p.keys.allow(key.AccessKey, rate)
	}
	return p.clients.allow(ClientGroup(httpx.ClientIP(r)), p.AnonymousRPS)
}

// ClientGroup returns the address used for counting: IPv4 addresses as
// they are, IPv6 addresses by their /64 network (one home or server).
func ClientGroup(ip string) string {
	p := net.ParseIP(ip)
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// maxEntries bounds memory: beyond it, full (idle) buckets are dropped.
const maxEntries = 50_000

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func (l *limiter) allow(id string, rate int) (bool, time.Duration) {
	if rate <= 0 {
		return true, 0
	}
	burst := float64(2 * rate)
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	b, ok := l.buckets[id]
	if !ok {
		if len(l.buckets) >= maxEntries {
			l.prune(now, burst, float64(rate))
		}
		b = &bucket{tokens: burst, last: now}
		l.buckets[id] = b
	}
	b.tokens = math.Min(burst, b.tokens+now.Sub(b.last).Seconds()*float64(rate))
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / float64(rate) * float64(time.Second))
	return false, max(wait, time.Millisecond)
}

// prune drops buckets that have refilled completely (nobody is close to the
// limit there); if none have, it drops the oldest half.
func (l *limiter) prune(now time.Time, burst, rate float64) {
	for id, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*rate >= burst {
			delete(l.buckets, id)
		}
	}
	if len(l.buckets) < maxEntries {
		return
	}
	cut := now.Add(-time.Second)
	for id, b := range l.buckets {
		if b.last.Before(cut) || len(l.buckets) > maxEntries/2 {
			delete(l.buckets, id)
		}
		if len(l.buckets) <= maxEntries/2 {
			return
		}
	}
}
