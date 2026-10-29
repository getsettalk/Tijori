package admin

import "testing"

func TestLimitKeyGroupsIPv6(t *testing.T) {
	if limitKey("2001:db8:1:2:aaaa::1") != limitKey("2001:db8:1:2:bbbb::9") {
		t.Fatal("same /64 should share a counter")
	}
	if limitKey("2001:db8:1:2::1") == limitKey("2001:db8:1:3::1") {
		t.Fatal("different /64s must not share")
	}
	if limitKey("203.0.113.7") != "ip:203.0.113.7" {
		t.Fatal("IPv4 unchanged")
	}
}

func TestUnknownUsersAreNotTracked(t *testing.T) {
	var l limiter
	for i := 0; i < 20000; i++ {
		l.fail("203.0.113.1", "") // unknown user names are passed as ""
	}
	if len(l.fails) != 1 {
		t.Fatalf("%d entries", len(l.fails))
	}
}
