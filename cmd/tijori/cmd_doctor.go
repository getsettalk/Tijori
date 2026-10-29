package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/server"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/sysinfo"
	"github.com/getsettalk/Tijori/internal/version"
)

type checker struct{ problems, warnings int }

func (c *checker) ok(format string, a ...any) { fmt.Printf("  [ ok ] "+format+"\n", a...) }
func (c *checker) warn(format string, a ...any) {
	c.warnings++
	fmt.Printf("  [warn] "+format+"\n", a...)
}
func (c *checker) fail(format string, a ...any) {
	c.problems++
	fmt.Printf("  [FAIL] "+format+"\n", a...)
}

func cmdDoctor(args []string) error {
	fs, cfgPath := newFlags("doctor", "doctor [--config tijori.toml]")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	c := &checker{}
	fmt.Println(version.String())
	fmt.Println()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		c.fail("config: %v", err)
		fmt.Println("\nFix the config problem above, then run `tijori doctor` again.")
		return fmt.Errorf("%d problem(s) found", c.problems)
	}
	if cfg.File != "" {
		c.ok("config file %s is valid", cfg.File)
		if fi, err := os.Stat(cfg.File); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 && (cfg.SecretKey != "" || cfg.Admin.Password != "") {
			c.warn("config file contains secrets but is readable by others; run: chmod 600 %s", cfg.File)
		}
	} else {
		c.ok("no config file; using defaults and TIJORI_* environment variables")
	}
	for _, w := range cfg.Warnings() {
		c.warn("%s", w)
	}
	if cfg.PublicURL == "" {
		c.warn("public_url is not set; links in the admin panel will use the address you browse with")
	}

	// Data directory.
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		c.fail("data folder %s cannot be created: %v", cfg.DataDir, err)
	} else {
		probe := filepath.Join(cfg.DataDir, ".doctor-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			c.fail("data folder %s is not writable by this user: %v", cfg.DataDir, err)
		} else {
			os.Remove(probe)
			c.ok("data folder %s is writable", cfg.DataDir)
		}
		if free := sysinfo.DiskFree(cfg.DataDir); free >= 0 {
			switch {
			case free < 1<<30:
				c.fail("only %s free disk space left", config.FormatBytes(free))
			case free < 10<<30:
				c.warn("%s free disk space", config.FormatBytes(free))
			default:
				c.ok("%s free disk space", config.FormatBytes(free))
			}
		}
		dbPath := filepath.Join(cfg.DataDir, state.FileName)
		if fi, err := os.Stat(dbPath); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			c.warn("%s should have 0600 permissions; run: chmod 600 %s", dbPath, dbPath)
		}
	}

	if db, store, err := server.Open(cfg); err != nil {
		c.fail("cannot open data: %v", err)
	} else {
		db.SetEnvKey(cfg.AccessKey, cfg.SecretKey)
		if n := len(db.Keys()); n == 0 {
			c.warn("no access keys; create one with: tijori key create --name my-app")
		} else {
			c.ok("%d access key(s)", n)
		}
		if cfg.Admin.Enabled {
			if len(db.Users()) == 0 && cfg.Admin.Password == "" {
				c.warn("admin panel has no user; run `tijori init` or set TIJORI_ADMIN_PASSWORD")
			} else {
				c.ok("admin panel enabled at %s/", cfg.Admin.Path)
			}
		}
		if b, err := store.ListBuckets(); err == nil {
			c.ok("%d bucket(s)", len(b))
		}
	}

	// ffmpeg.
	ff := cfg.Video.FFmpeg
	if ff == "" {
		ff, _ = exec.LookPath("ffmpeg")
	}
	if ff == "" {
		c.ok("ffmpeg not found: video processing is off (optional)")
	} else if out, err := exec.Command(ff, "-version").Output(); err != nil {
		c.warn("ffmpeg at %s does not run: %v", ff, err)
	} else {
		line := string(out)
		if i := strings.IndexByte(line, '\n'); i > 0 {
			line = line[:i]
		}
		c.ok("ffmpeg found: %s", line)
	}

	// Port.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		hc := http.Client{Timeout: 2 * time.Second}
		if resp, herr := hc.Get(localURL(cfg) + "/health"); herr == nil {
			resp.Body.Close()
			c.ok("port %s is in use by a running Tijori (health check answered %d)", cfg.Listen, resp.StatusCode)
		} else {
			c.fail("cannot listen on %s: %v", cfg.Listen, err)
		}
	} else {
		ln.Close()
		c.ok("port %s is free", cfg.Listen)
	}
	if cfg.Backup.To == "" {
		c.warn("no backup destination configured ([backup] to = ...)")
	} else {
		c.ok("backups go to %s", cfg.Redacted().Backup.To)
	}

	fmt.Println()
	if c.problems > 0 {
		return fmt.Errorf("%d problem(s) and %d warning(s) found", c.problems, c.warnings)
	}
	if c.warnings > 0 {
		fmt.Printf("No problems, %d warning(s).\n", c.warnings)
	} else {
		fmt.Println("Everything looks good.")
	}
	return nil
}

// cmdHealthcheck is used by the Docker HEALTHCHECK (the image has no curl).
func cmdHealthcheck(args []string) error {
	fs, cfgPath := newFlags("healthcheck", "healthcheck")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}
