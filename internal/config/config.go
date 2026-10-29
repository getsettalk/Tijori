// Package config loads Tijori configuration from a TOML file and TIJORI_*
// environment variables, applies defaults and validates the result.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the full server configuration.
type Config struct {
	Listen        string `toml:"listen"`
	DataDir       string `toml:"data_dir"`
	PublicURL     string `toml:"public_url"`
	Region        string `toml:"region"`
	MaxTotal      Size   `toml:"max_total"`
	MaxObjectSize Size   `toml:"max_object_size"`
	LogFormat     string `toml:"log_format"`
	LogLevel      string `toml:"log_level"`
	Metrics       bool   `toml:"metrics"`
	// ErrorFormat is "auto" (XML, or JSON when the client sends
	// Accept: application/json), "xml" or "json".
	ErrorFormat string `toml:"error_format"`
	// CORSOrigins lists web origins whose JavaScript may call the S3 API and
	// /img route (for example presigned uploads from a browser). Empty turns
	// CORS off; "*" allows any origin.
	CORSOrigins []string `toml:"cors_origins"`
	// TrustProxyHeaders uses X-Forwarded-For from private/loopback peers to
	// find the real client address (rate limits, logs). Default true.
	TrustProxyHeaders bool `toml:"trust_proxy_headers"`
	// CheckUpdates lets the admin panel show when a newer Tijori release is
	// out (one anonymous request to the GitHub API every 12 hours).
	CheckUpdates bool `toml:"check_updates"`

	// AccessKey and SecretKey define an optional root key with full access.
	// Normally set through TIJORI_ACCESS_KEY / TIJORI_SECRET_KEY (Docker).
	AccessKey string `toml:"access_key"`
	SecretKey string `toml:"secret_key"`

	Admin   AdminConfig             `toml:"admin"`
	Buckets map[string]BucketConfig `toml:"buckets"`
	Images  ImagesConfig            `toml:"images"`
	Video   VideoConfig             `toml:"video"`
	Backup  BackupConfig            `toml:"backup"`

	// File is the config file that was loaded, empty when running on
	// defaults and environment variables only.
	File string `toml:"-"`
}

type AdminConfig struct {
	Enabled bool   `toml:"enabled"`
	Path    string `toml:"path"`
	// Listen, when set, serves the admin panel on a separate address
	// (for example "127.0.0.1:9001") instead of the main listener.
	Listen string `toml:"listen"`
	// Password sets or resets the "admin" user's password at startup.
	Password string `toml:"password"`
}

type BucketConfig struct {
	PublicRead    bool `toml:"public_read"`
	MaxSize       Size `toml:"max_size"`
	MaxObjectSize Size `toml:"max_object_size"`
	VideoProcess  bool `toml:"video_process"`
}

type ImagesConfig struct {
	Enabled       bool  `toml:"enabled"`
	MaxWidth      int   `toml:"max_width"`
	MaxHeight     int   `toml:"max_height"`
	MaxPixels     int64 `toml:"max_pixels"`
	AllowedWidths []int `toml:"allowed_widths"`
	Quality       int   `toml:"quality"`
	CacheSize     Size  `toml:"cache_size"`
	MaxInputSize  Size  `toml:"max_input_size"`
}

type VideoConfig struct {
	FFmpeg        string   `toml:"ffmpeg"`
	Workers       int      `toml:"workers"`
	Webhook       string   `toml:"webhook"`
	WebhookSecret string   `toml:"webhook_secret"`
	Presets       []string `toml:"presets"`
}

type BackupConfig struct {
	To       string `toml:"to"`
	Schedule string `toml:"schedule"`
	Keep     int    `toml:"keep"`
}

// Defaults returns a configuration with every default applied.
func Defaults() *Config {
	return &Config{
		Listen:            ":9000",
		DataDir:           "./data",
		Region:            "auto",
		MaxObjectSize:     5 << 30,
		LogFormat:         "text",
		LogLevel:          "info",
		ErrorFormat:       "auto",
		TrustProxyHeaders: true,
		CheckUpdates:      true,
		Admin:             AdminConfig{Enabled: true, Path: "/_admin"},
		Buckets:           map[string]BucketConfig{},
		Images: ImagesConfig{
			Enabled:      true,
			MaxWidth:     4000,
			MaxHeight:    4000,
			MaxPixels:    50_000_000,
			Quality:      80,
			CacheSize:    500 << 20,
			MaxInputSize: 50 << 20,
		},
		Video:  VideoConfig{Workers: 1, Presets: []string{"thumbnail", "720p"}},
		Backup: BackupConfig{Keep: 7},
	}
}

// SearchPaths lists the config files tried, in order, when none is given.
func SearchPaths() []string {
	return []string{"./tijori.toml", "/etc/tijori/tijori.toml"}
}

// Load reads configuration. path may be empty, in which case TIJORI_CONFIG
// and then SearchPaths are tried. A missing config file is not an error:
// Tijori then runs on defaults plus environment variables.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	if path == "" {
		path = os.Getenv("TIJORI_CONFIG")
	}
	explicit := path != ""
	if !explicit {
		for _, p := range SearchPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path != "" {
		md, err := toml.DecodeFile(path, cfg)
		if err != nil {
			if explicit && errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("config file %s not found (run `tijori init` to create one)", path)
			}
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if und := md.Undecoded(); len(und) > 0 {
			keys := make([]string, len(und))
			for i, k := range und {
				keys[i] = k.String()
			}
			return nil, fmt.Errorf("%s: unknown setting(s): %s", path, strings.Join(keys, ", "))
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		cfg.File = path
		// Relative data_dir in a config file is relative to that file.
		if cfg.DataDir != "" && !filepath.IsAbs(cfg.DataDir) && md.IsDefined("data_dir") {
			cfg.DataDir = filepath.Join(filepath.Dir(path), cfg.DataDir)
		}
	}
	if err := applyEnv(cfg, os.Environ()); err != nil {
		return nil, err
	}
	if cfg.Buckets == nil {
		cfg.Buckets = map[string]BucketConfig{}
	}
	if abs, err := filepath.Abs(cfg.DataDir); err == nil {
		cfg.DataDir = abs
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// ReservedBucketNames clash with Tijori's own routes.
var ReservedBucketNames = map[string]bool{"img": true, "health": true, "metrics": true}

// ValidBucketName reports whether name is an acceptable S3 bucket name.
func ValidBucketName(name string) bool {
	if !bucketNameRE.MatchString(name) || ReservedBucketNames[name] {
		return false
	}
	if strings.Contains(name, "..") || net.ParseIP(name) != nil {
		return false
	}
	return true
}

// Validate checks the configuration for mistakes and returns the first one.
func (c *Config) Validate() error {
	if c.Listen == "" {
		return errors.New("listen must not be empty (example: \":9000\")")
	}
	if c.DataDir == "" {
		return errors.New("data_dir must not be empty")
	}
	if !strings.HasPrefix(c.Admin.Path, "/") || c.Admin.Path == "/" || strings.HasSuffix(c.Admin.Path, "/") {
		return fmt.Errorf("admin.path %q must start with / and not end with / (example: \"/_admin\")", c.Admin.Path)
	}
	first := strings.SplitN(strings.TrimPrefix(c.Admin.Path, "/"), "/", 2)[0]
	if ValidBucketName(first) {
		return fmt.Errorf("admin.path %q could clash with a bucket name; start it with an underscore, for example /_admin", c.Admin.Path)
	}
	for name := range c.Buckets {
		if !ValidBucketName(name) {
			return fmt.Errorf("[buckets.%s]: invalid bucket name (use 3-63 lowercase letters, digits, dots or hyphens)", name)
		}
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("public_url %q must look like https://files.example.com", c.PublicURL)
		}
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("log_format must be \"text\" or \"json\", got %q", c.LogFormat)
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be debug, info, warn or error, got %q", c.LogLevel)
	}
	switch c.ErrorFormat {
	case "auto", "xml", "json":
	case "":
		c.ErrorFormat = "auto"
	default:
		return fmt.Errorf("error_format must be \"auto\", \"xml\" or \"json\", got %q", c.ErrorFormat)
	}
	for _, o := range c.CORSOrigins {
		if o == "*" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("cors_origins: %q must look like https://app.example.com (or \"*\")", o)
		}
	}
	if (c.AccessKey == "") != (c.SecretKey == "") {
		return errors.New("set both TIJORI_ACCESS_KEY and TIJORI_SECRET_KEY, or neither")
	}
	if c.SecretKey != "" && len(c.SecretKey) < 8 {
		return errors.New("secret key must be at least 8 characters")
	}
	if c.Video.Workers < 1 {
		c.Video.Workers = 1
	}
	if c.Images.Quality <= 0 || c.Images.Quality > 100 {
		c.Images.Quality = 80
	}
	if c.Backup.Schedule != "" {
		if _, err := ParseSchedule(c.Backup.Schedule); err != nil {
			return err
		}
	}
	return nil
}

// Warnings returns non-fatal problems worth logging at startup.
func (c *Config) Warnings() []string {
	var w []string
	if c.ErrorFormat == "json" {
		w = append(w, `error_format = "json": S3 SDKs (Laravel/Flysystem, AWS SDKs, boto3) expect XML errors and may not recognise codes like NoSuchKey; prefer "auto" and send "Accept: application/json" from clients that want JSON`)
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err == nil && u.Scheme == "http" && !isLocalHost(u.Hostname()) {
			w = append(w, "public_url uses plain http; use https in production so keys and files are encrypted in transit")
		}
	}
	return w
}

func isLocalHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// Bucket returns the config-file settings for a bucket and whether the
// bucket has a section in the config file.
func (c *Config) Bucket(name string) (BucketConfig, bool) {
	b, ok := c.Buckets[name]
	return b, ok
}

// Schedule is a parsed backup schedule: "daily 03:00", "hourly",
// "weekly sun 03:00" or "every 6h".
type Schedule struct {
	Every   time.Duration // for "every" and "hourly"
	Weekday int           // -1 for daily
	Hour    int
	Minute  int
}

// ParseSchedule parses a backup schedule string.
func ParseSchedule(s string) (Schedule, error) {
	f := strings.Fields(strings.ToLower(s))
	bad := fmt.Errorf("backup.schedule %q not understood (examples: \"daily 03:00\", \"weekly sun 03:00\", \"every 6h\", \"hourly\")", s)
	if len(f) == 0 {
		return Schedule{}, bad
	}
	parseHM := func(v string) (int, int, bool) {
		var h, m int
		if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, 0, false
		}
		return h, m, true
	}
	switch f[0] {
	case "hourly":
		return Schedule{Every: time.Hour, Weekday: -1}, nil
	case "every":
		if len(f) != 2 {
			return Schedule{}, bad
		}
		d, err := time.ParseDuration(f[1])
		if err != nil || d < time.Minute {
			return Schedule{}, bad
		}
		return Schedule{Every: d, Weekday: -1}, nil
	case "daily":
		hm := "03:00"
		if len(f) == 2 {
			hm = f[1]
		} else if len(f) > 2 {
			return Schedule{}, bad
		}
		h, m, ok := parseHM(hm)
		if !ok {
			return Schedule{}, bad
		}
		return Schedule{Weekday: -1, Hour: h, Minute: m}, nil
	case "weekly":
		if len(f) != 3 {
			return Schedule{}, bad
		}
		days := map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
		wd, ok := days[f[1][:min(3, len(f[1]))]]
		if !ok {
			return Schedule{}, bad
		}
		h, m, ok := parseHM(f[2])
		if !ok {
			return Schedule{}, bad
		}
		return Schedule{Weekday: wd, Hour: h, Minute: m}, nil
	}
	return Schedule{}, bad
}

// Next returns the next run time strictly after t.
func (s Schedule) Next(t time.Time) time.Time {
	if s.Every > 0 {
		return t.Truncate(s.Every).Add(s.Every)
	}
	next := time.Date(t.Year(), t.Month(), t.Day(), s.Hour, s.Minute, 0, 0, t.Location())
	for !next.After(t) || (s.Weekday >= 0 && int(next.Weekday()) != s.Weekday) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// applyEnv overrides fields from TIJORI_* variables. Names are the TOML path
// in upper case joined with underscores, for example TIJORI_IMAGES_MAX_WIDTH.
// Buckets use TIJORI_BUCKETS_<NAME>_<FIELD>, with "_" in NAME meaning "-".
// TIJORI_PUBLIC_BUCKETS=a,b is a shortcut that marks buckets public.
func applyEnv(cfg *Config, environ []string) error {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "TIJORI_") {
			env[k] = v
		}
	}
	if err := applyEnvStruct(reflect.ValueOf(cfg).Elem(), "TIJORI", env); err != nil {
		return err
	}
	if v, ok := env["TIJORI_PUBLIC_BUCKETS"]; ok {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				b := cfg.Buckets[name]
				b.PublicRead = true
				if cfg.Buckets == nil {
					cfg.Buckets = map[string]BucketConfig{}
				}
				cfg.Buckets[name] = b
			}
		}
	}
	bt := reflect.TypeOf(BucketConfig{})
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rest, ok := strings.CutPrefix(k, "TIJORI_BUCKETS_")
		if !ok {
			continue
		}
		matched := false
		for i := 0; i < bt.NumField(); i++ {
			suffix := "_" + strings.ToUpper(bt.Field(i).Tag.Get("toml"))
			if name, ok := strings.CutSuffix(rest, suffix); ok && name != "" {
				bucket := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
				if cfg.Buckets == nil {
					cfg.Buckets = map[string]BucketConfig{}
				}
				b := cfg.Buckets[bucket]
				if err := setField(reflect.ValueOf(&b).Elem().Field(i), env[k]); err != nil {
					return fmt.Errorf("%s: %w", k, err)
				}
				cfg.Buckets[bucket] = b
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: unknown bucket setting", k)
		}
	}
	return nil
}

func applyEnvStruct(v reflect.Value, prefix string, env map[string]string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := prefix + "_" + strings.ToUpper(tag)
		fv := v.Field(i)
		if f.Type.Kind() == reflect.Struct {
			if err := applyEnvStruct(fv, name, env); err != nil {
				return err
			}
			continue
		}
		if f.Type.Kind() == reflect.Map {
			continue
		}
		if val, ok := env[name]; ok {
			if err := setField(fv, val); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

var sizeType = reflect.TypeOf(Size(0))

func setField(fv reflect.Value, val string) error {
	if fv.Type() == sizeType {
		s, err := ParseSize(val)
		if err != nil {
			return err
		}
		fv.SetInt(int64(s))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(val)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("expected true or false, got %q", val)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return fmt.Errorf("expected a number, got %q", val)
		}
		fv.SetInt(n)
	case reflect.Slice:
		parts := strings.Split(val, ",")
		s := reflect.MakeSlice(fv.Type(), 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			ev := reflect.New(fv.Type().Elem()).Elem()
			if err := setField(ev, p); err != nil {
				return err
			}
			s = reflect.Append(s, ev)
		}
		fv.Set(s)
	default:
		return fmt.Errorf("unsupported setting type %s", fv.Kind())
	}
	return nil
}

// Redacted returns a copy safe to display: secrets are masked.
func (c *Config) Redacted() *Config {
	cp := *c
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		return "********"
	}
	cp.SecretKey = mask(c.SecretKey)
	cp.Admin.Password = mask(c.Admin.Password)
	cp.Video.WebhookSecret = mask(c.Video.WebhookSecret)
	if u, err := url.Parse(c.Video.Webhook); err == nil && (u.User != nil || u.RawQuery != "") {
		u.User = nil
		if u.RawQuery != "" {
			u.RawQuery = "********"
		}
		cp.Video.Webhook = u.String()
	}
	if u, err := url.Parse(c.Backup.To); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "********")
			cp.Backup.To = u.String()
		}
	}
	q := strings.Index(cp.Backup.To, "?")
	if q >= 0 {
		vals, err := url.ParseQuery(cp.Backup.To[q+1:])
		if err == nil {
			for _, k := range []string{"secret_key", "secret", "password"} {
				if vals.Has(k) {
					vals.Set(k, "********")
				}
			}
			cp.Backup.To = cp.Backup.To[:q+1] + vals.Encode()
		}
	}
	return &cp
}

// TOML renders the configuration as TOML text.
func (c *Config) TOML() string {
	var b strings.Builder
	_ = toml.NewEncoder(&b).Encode(c)
	return b.String()
}
