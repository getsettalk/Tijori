// Package updates checks GitHub for the latest Tijori release so the admin
// panel can say whether this server is up to date. It sends one anonymous
// GET request every 12 hours and nothing else; turn it off with
// check_updates = false.
package updates

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the GitHub API endpoint for the newest release.
const DefaultURL = "https://api.github.com/repos/getsettalk/Tijori/releases/latest"

// releasePage is where release notes live. Links from the API response are
// only used when they point here, so a tampered response cannot put another
// address into the admin panel.
const releasePage = "https://github.com/getsettalk/Tijori/releases/"

// Status is what the admin panel shows.
type Status struct {
	Enabled   bool       `json:"enabled"`
	Current   string     `json:"current"`
	Latest    string     `json:"latest,omitempty"`
	URL       string     `json:"url,omitempty"`
	Available bool       `json:"available"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// Checker caches the latest release.
type Checker struct {
	Current  string
	URL      string // defaults to DefaultURL
	Interval time.Duration
	Client   *http.Client
	Log      *slog.Logger

	mu     sync.Mutex
	latest string
	link   string
	at     time.Time
}

// Run checks now and then every Interval until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = 12 * time.Hour
	}
	for {
		c.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Check asks GitHub once. Errors are ignored: a server without internet
// access simply shows no update information.
func (c *Checker) Check(ctx context.Context) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	url := c.URL
	if url == "" {
		url = DefaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Tijori/"+c.Current)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Link       string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel) != nil || rel.Draft || rel.Prerelease {
		return
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	if _, ok := parse(latest); !ok {
		return
	}
	link := rel.Link
	if !strings.HasPrefix(link, releasePage) {
		link = releasePage + "tag/v" + latest
	}
	c.mu.Lock()
	changed := latest != c.latest
	c.latest, c.link, c.at = latest, link, time.Now().UTC()
	c.mu.Unlock()
	if changed && c.Log != nil && Newer(latest, c.Current) {
		c.Log.Info("a newer Tijori release is available", "current", c.Current, "latest", latest, "notes", link)
	}
}

// Status returns the cached result.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Enabled: true, Current: strings.TrimPrefix(c.Current, "v"), Latest: c.latest, URL: c.link}
	if !c.at.IsZero() {
		at := c.at
		s.CheckedAt = &at
	}
	s.Available = c.latest != "" && Newer(c.latest, s.Current)
	return s
}

// Newer reports whether version a is greater than b ("1.2.0" > "1.1.9").
// Versions that are not plain X.Y.Z numbers, such as "dev", never compare
// as newer or older.
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// parse reads "1.2.3", ignoring a leading "v" and any "-pre" or "+build"
// suffix.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || len(p) > 6 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
