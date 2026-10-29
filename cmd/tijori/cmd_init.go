package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/server"
	"github.com/getsettalk/Tijori/internal/state"
)

const configTemplate = `# Tijori configuration.
# Every setting can also be set with an environment variable named TIJORI_
# plus the setting in capitals, for example TIJORI_LISTEN or
# TIJORI_IMAGES_MAX_WIDTH. Environment variables win over this file.
# Reference: https://github.com/getsettalk/Tijori/blob/main/docs/configuration.md

listen   = %q
data_dir = %q
%s
# max_total       = "100GB"   # limit for all buckets together
# max_object_size = "5GB"     # largest single file (default 5GB)
# log_format      = "text"    # or "json"
# metrics         = false     # expose /metrics for Prometheus
# error_format    = "auto"    # "auto": XML, or JSON for clients sending Accept: application/json
#                             # "xml": always XML. "json": always JSON (S3 SDKs may not understand it)

[admin]
enabled = true
path    = "/_admin"
# listen = "127.0.0.1:9001"   # serve the admin panel on its own address only

# Per-bucket settings. Buckets listed here are created at startup, and their
# settings can then only be changed in this file (not in the admin panel).
#
# [buckets.photos]
# public_read     = true      # anyone may download (never upload)
# max_size        = "10GB"    # bucket quota
# max_object_size = "20MB"
#
# [buckets.videos]
# video_process = true        # make a thumbnail and 720p version on upload

[images]
enabled   = true
max_width = 4000
max_height = 4000
quality   = 80
cache_size = "500MB"
# allowed_widths = [150, 300, 600, 1200]   # stop people filling the cache

[video]
ffmpeg  = ""        # empty = find ffmpeg on PATH
workers = 1
# webhook        = "https://yourapp.com/hooks/tijori"
# webhook_secret = "change-me"

[backup]
# to       = "/mnt/backup/tijori"   # or sftp://user@host/path or s3://bucket?endpoint=https://...
# schedule = "daily 03:00"
keep = 7
`

func defaultPaths() (cfgPath, dataDir string) {
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		return "/etc/tijori/tijori.toml", "/var/lib/tijori"
	}
	return "tijori.toml", "data"
}

func cmdInit(args []string) error {
	fs, cfgPath := newFlags("init", "init [--config tijori.toml] [--data-dir DIR] [--admin-password PW] [--yes]")
	dataDir := fs.String("data-dir", "", "where files are stored (default ./data, or /var/lib/tijori as root on Linux)")
	listen := fs.String("listen", ":9000", "address to listen on")
	publicURL := fs.String("public-url", "", "public address of this server, e.g. https://files.example.com")
	adminPW := fs.String("admin-password", "", "admin panel password (default: ask, or generate one)")
	yes := fs.Bool("yes", false, "do not ask questions; generate a password if none is given")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	defCfg, defData := defaultPaths()
	if *cfgPath == "" {
		*cfgPath = defCfg
	}
	if *dataDir == "" {
		*dataDir = defData
	}

	created := false
	if _, err := os.Stat(*cfgPath); err == nil {
		fmt.Printf("Found existing config %s; keeping it.\n", *cfgPath)
	} else {
		if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", filepath.Dir(*cfgPath), err)
		}
		pub := "# public_url = \"https://files.example.com\"   # the address your apps and users reach"
		if *publicURL != "" {
			pub = fmt.Sprintf("public_url = %q", *publicURL)
		}
		body := fmt.Sprintf(configTemplate, *listen, *dataDir, pub)
		if err := os.WriteFile(*cfgPath, []byte(body), 0o600); err != nil {
			return fmt.Errorf("cannot write %s: %w", *cfgPath, err)
		}
		created = true
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	db, _, err := server.Open(cfg)
	if err != nil {
		return err
	}

	var password string
	if len(db.Users()) == 0 {
		password = *adminPW
		if password == "" {
			password = os.Getenv("TIJORI_ADMIN_PASSWORD")
		}
		if password == "" && !*yes && term.IsTerminal(int(os.Stdin.Fd())) {
			password, err = askPassword()
			if err != nil {
				return err
			}
		}
		if password == "" {
			password = auth.RandomString(20, "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789")
		}
		if err := server.EnsureAdminPassword(db, "admin", password); err != nil {
			return err
		}
	}

	var key *state.AccessKey
	if !hasStoredKeys(db) {
		k, err := auth.NewKey("default", []string{"*"}, state.PermReadWrite)
		if err != nil {
			return err
		}
		if err := db.AddKey(k); err != nil {
			return err
		}
		key = &k
	}

	base := localURL(cfg)
	fmt.Println()
	fmt.Println("Tijori is ready.")
	fmt.Println()
	if created {
		fmt.Printf("  Config file:   %s\n", cfg.File)
	} else {
		fmt.Printf("  Config file:   %s (unchanged)\n", cfg.File)
	}
	fmt.Printf("  Data folder:   %s\n", cfg.DataDir)
	fmt.Printf("  S3 endpoint:   %s\n", base)
	if cfg.Admin.Enabled {
		fmt.Printf("  Admin panel:   %s%s/\n", base, cfg.Admin.Path)
	}
	if password != "" {
		fmt.Printf("  Admin login:   admin / %s\n", password)
	} else {
		fmt.Printf("  Admin login:   already set (reset with: tijori admin password)\n")
	}
	if key != nil {
		fmt.Println()
		fmt.Printf("  Access key:    %s\n", key.AccessKey)
		fmt.Printf("  Secret key:    %s\n", key.SecretKey)
		fmt.Println("  Save the secret now. It is not shown again (you can always create new keys).")
	}
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Start the server:    tijori serve" + configFlag(cfg))
	fmt.Println("  2. Check it works:      curl " + base + "/health")
	fmt.Println("  3. Create a bucket:     tijori bucket create my-files" + configFlag(cfg))
	fmt.Println("  4. Connect your app:    endpoint " + base + ", region \"auto\", path-style URLs on")
	if runtime.GOOS == "linux" {
		fmt.Println("  To start on boot:       sudo tijori service install")
	}
	return nil
}

func configFlag(cfg *config.Config) string {
	if cfg.File == "" {
		return ""
	}
	for _, p := range config.SearchPaths() {
		if abs, _ := filepath.Abs(p); abs == cfg.File {
			return ""
		}
	}
	return " --config " + cfg.File
}

func hasStoredKeys(db *state.DB) bool {
	for _, k := range db.Keys() {
		if !k.FromEnv {
			return true
		}
	}
	return false
}

func localURL(cfg *config.Config) string {
	if cfg.PublicURL != "" {
		return strings.TrimSuffix(cfg.PublicURL, "/")
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return "http://localhost:9000"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func askPassword() (string, error) {
	for {
		fmt.Print("Choose an admin panel password (at least 8 characters, or press Enter to generate one): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		pw := string(b)
		if pw == "" {
			return "", nil
		}
		if len(pw) < 8 {
			fmt.Println("That is too short; please use at least 8 characters.")
			continue
		}
		fmt.Print("Type it again: ")
		b2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		if string(b2) != pw {
			fmt.Println("The passwords do not match; try again.")
			continue
		}
		return pw, nil
	}
}

func cmdAdmin(args []string) error {
	if len(args) == 0 || (args[0] != "password" && args[0] != "passwd") {
		return errors.New("usage: tijori admin password [--user admin] [--password PW] [--readonly]")
	}
	fs, cfgPath := newFlags("admin password", "admin password [--user admin] [--password PW] [--readonly]")
	user := fs.String("user", "admin", "user name")
	pw := fs.String("password", "", "new password (default: ask)")
	readonly := fs.Bool("readonly", false, "create the user as a read-only admin")
	if _, err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	db, _, err := server.Open(cfg)
	if err != nil {
		return err
	}
	if *pw == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			r := bufio.NewReader(os.Stdin)
			line, _ := r.ReadString('\n')
			*pw = strings.TrimSpace(line)
		} else if *pw, err = askPassword(); err != nil {
			return err
		}
	}
	if *pw == "" {
		return errors.New("no password given")
	}
	_, existed := db.User(*user)
	if err := server.EnsureAdminPassword(db, *user, *pw); err != nil {
		return err
	}
	if *readonly {
		u, _ := db.User(*user)
		u.Role = "readonly"
		if err := db.PutUser(u); err != nil {
			return err
		}
	}
	if existed {
		fmt.Printf("Password for %q updated.\n", *user)
	} else {
		fmt.Printf("Admin user %q created.\n", *user)
	}
	return nil
}
