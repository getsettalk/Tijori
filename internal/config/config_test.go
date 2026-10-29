package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := map[string]Size{"": 0, "1024": 1024, "1KB": 1024, "10GB": 10 << 30, "1.5 MB": 1536 << 10, "500mb": 500 << 20, "2TiB": 2 << 40}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "-1GB", "10XB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tijori.toml")
	os.WriteFile(p, []byte(`
listen = ":9100"
data_dir = "store"
max_total = "100GB"
[buckets.photos]
public_read = true
max_size = "10GB"
[images]
max_width = 2000
`), 0o600)
	t.Setenv("TIJORI_LISTEN", ":9200")
	t.Setenv("TIJORI_IMAGES_ALLOWED_WIDTHS", "150, 300")
	t.Setenv("TIJORI_BUCKETS_MY_VIDEOS_VIDEO_PROCESS", "true")
	t.Setenv("TIJORI_PUBLIC_BUCKETS", "assets")
	t.Setenv("TIJORI_CHECK_UPDATES", "false")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9200" {
		t.Errorf("env should win: listen %q", cfg.Listen)
	}
	if cfg.DataDir != filepath.Join(dir, "store") {
		t.Errorf("relative data_dir not resolved against config file: %s", cfg.DataDir)
	}
	if cfg.MaxTotal != 100<<30 || cfg.Images.MaxWidth != 2000 || len(cfg.Images.AllowedWidths) != 2 {
		t.Errorf("file values: %+v", cfg)
	}
	if !cfg.Buckets["photos"].PublicRead || !cfg.Buckets["my-videos"].VideoProcess || !cfg.Buckets["assets"].PublicRead {
		t.Errorf("buckets: %+v", cfg.Buckets)
	}
	if cfg.CheckUpdates || !Defaults().CheckUpdates {
		t.Errorf("check_updates: env false gave %v, default should be true", cfg.CheckUpdates)
	}
}

func TestUnknownSettingRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tijori.toml")
	os.WriteFile(p, []byte("listn = \":9000\"\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("typo in setting name should be reported")
	}
}

func TestSchedule(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, loc) // a Thursday
	cases := map[string]time.Time{
		"daily 03:00":      time.Date(2026, 10, 30, 3, 0, 0, 0, loc),
		"daily 23:30":      time.Date(2026, 10, 29, 23, 30, 0, 0, loc),
		"weekly sun 04:00": time.Date(2026, 11, 1, 4, 0, 0, 0, loc),
		"hourly":           time.Date(2026, 10, 29, 11, 0, 0, 0, loc),
	}
	for in, want := range cases {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := s.Next(now); !got.Equal(want) {
			t.Errorf("%s: next %v want %v", in, got, want)
		}
	}
	if _, err := ParseSchedule("sometimes"); err == nil {
		t.Error("bad schedule accepted")
	}
}
