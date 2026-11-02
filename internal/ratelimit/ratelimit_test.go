package ratelimit

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getsettalk/Tijori/internal/state"
)

func TestAllow(t *testing.T) {
	var p *Policy
	if ok, _ := p.Allow(httptest.NewRequest("GET", "/", nil), nil); !ok {
		t.Fatal("a nil policy must allow everything")
	}
	p = &Policy{KeyRPS: 5, AnonymousRPS: 2}
	key := &state.AccessKey{AccessKey: "tj_A"}
	// A burst of twice the rate passes, the next request waits.
	for i := 0; i < 10; i++ {
		if ok, _ := p.Allow(httptest.NewRequest("GET", "/", nil), key); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait := p.Allow(httptest.NewRequest("GET", "/", nil), key)
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("over the limit: ok=%v wait=%v", ok, wait)
	}
	time.Sleep(250 * time.Millisecond) // refills about one token
	if ok, _ := p.Allow(httptest.NewRequest("GET", "/", nil), key); !ok {
		t.Fatal("the bucket does not refill")
	}

	// A key's own limit wins; -1 means no limit.
	slow := &state.AccessKey{AccessKey: "tj_B", RateLimit: 1}
	p.Allow(httptest.NewRequest("GET", "/", nil), slow)
	p.Allow(httptest.NewRequest("GET", "/", nil), slow)
	if ok, _ := p.Allow(httptest.NewRequest("GET", "/", nil), slow); ok {
		t.Fatal("per-key limit ignored")
	}
	free := &state.AccessKey{AccessKey: "tj_C", RateLimit: -1}
	for i := 0; i < 100; i++ {
		if ok, _ := p.Allow(httptest.NewRequest("GET", "/", nil), free); !ok {
			t.Fatal("exempt key limited")
		}
	}

	// Anonymous requests count per address; IPv6 per /64.
	anon := func(addr string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr
		ok, _ := p.Allow(r, nil)
		return ok
	}
	for i := 0; i < 4; i++ {
		anon("203.0.113.5:1000")
	}
	if anon("203.0.113.5:1001") {
		t.Fatal("anonymous limit ignored")
	}
	if !anon("203.0.113.6:1000") {
		t.Fatal("another address shares the limit")
	}
	for i := 0; i < 4; i++ {
		anon("[2001:db8:1:2::1]:443")
	}
	if anon("[2001:db8:1:2::ffff]:443") {
		t.Fatal("IPv6 addresses in one /64 should share a limit")
	}
	if !anon("[2001:db8:1:3::1]:443") {
		t.Fatal("another /64 shares the limit")
	}
	// No limit configured: nothing is counted.
	if ok, _ := (&Policy{}).Allow(httptest.NewRequest("GET", "/", nil), key); !ok {
		t.Fatal("zero policy limited a request")
	}
}

func TestMemoryIsBounded(t *testing.T) {
	var l limiter
	for i := 0; i < maxEntries+5000; i++ {
		l.allow(time.Duration(i).String(), 10)
	}
	if n := len(l.buckets); n > maxEntries {
		t.Fatalf("%d buckets kept, limit %d", n, maxEntries)
	}
}
