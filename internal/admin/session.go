package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const (
	cookieName     = "tijori_session"
	sessionTTL     = 12 * time.Hour
	maxFailures    = 5
	failureWindow  = 5 * time.Minute
	maxUserFailure = 10
)

type session struct {
	user    string
	role    string
	csrf    string
	expires time.Time
}

// sessions keeps logged-in sessions in memory. A restart logs everyone out,
// which is acceptable for a small admin panel and keeps logout trustworthy.
type sessions struct {
	mu   sync.Mutex
	byID map[string]*session
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *sessions) create(user, role string) (string, *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = map[string]*session{}
	}
	now := time.Now()
	for id, se := range s.byID {
		if now.After(se.expires) {
			delete(s.byID, id)
		}
	}
	id := randomHex(32)
	se := &session{user: user, role: role, csrf: randomHex(16), expires: now.Add(sessionTTL)}
	s.byID[id] = se
	return id, se
}

func (s *sessions) get(id string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.byID[id]
	if !ok || time.Now().After(se.expires) {
		delete(s.byID, id)
		return nil, false
	}
	return se, true
}

func (s *sessions) delete(id string) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// dropUser ends every session of a user (after a password change or delete).
func (s *sessions) dropUser(user string) {
	s.mu.Lock()
	for id, se := range s.byID {
		if se.user == user {
			delete(s.byID, id)
		}
	}
	s.mu.Unlock()
}

// limiter counts failed logins per client IP and per user name.
type limiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < failureWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = keep
	}
	return keep
}

// blocked reports whether ip or user must wait, and for how long.
func (l *limiter) blocked(ip, user string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	now := time.Now()
	check := func(key string, max int) (bool, time.Duration) {
		r := l.recent(key, now)
		if len(r) >= max {
			return true, failureWindow - now.Sub(r[0])
		}
		return false, 0
	}
	if b, d := check("ip:"+ip, maxFailures); b {
		return b, d
	}
	return check("user:"+user, maxUserFailure)
}

func (l *limiter) fail(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	now := time.Now()
	l.fails["ip:"+ip] = append(l.recent("ip:"+ip, now), now)
	l.fails["user:"+user] = append(l.recent("user:"+user, now), now)
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, "ip:"+ip)
	l.mu.Unlock()
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func equalConst(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
