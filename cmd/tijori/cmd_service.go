package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/getsettalk/Tijori/internal/config"
)

const systemdUnit = `[Unit]
Description=Tijori S3-compatible storage
Documentation=https://github.com/getsettalk/Tijori
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=tijori
Group=tijori
ExecStart=%s serve --config %s
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
%s
# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.tijori</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>serve</string><string>--config</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cmdService(args []string) error {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		return errors.New("usage: tijori service install|uninstall")
	}
	sub := args[0]
	fs, cfgPath := newFlags("service "+sub, "service "+sub+" [--config /etc/tijori/tijori.toml]")
	if _, err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		if sub == "install" {
			return installSystemd(*cfgPath)
		}
		return uninstallSystemd()
	case "darwin":
		if sub == "install" {
			return installLaunchd(*cfgPath)
		}
		return uninstallLaunchd()
	}
	return fmt.Errorf("service install is not supported on %s; run `tijori serve` with your system's service manager", runtime.GOOS)
}

func installSystemd(cfgPath string) error {
	if os.Geteuid() != 0 {
		return errors.New("run this with sudo: sudo tijori service install")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemd was not found on this system")
	}
	if cfgPath == "" {
		cfgPath = "/etc/tijori/tijori.toml"
	}
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("%s does not exist yet; run `sudo tijori init` first", cfgPath)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)

	if err := run("id", "tijori"); err != nil {
		if err := run("useradd", "--system", "--home-dir", cfg.DataDir, "--no-create-home", "--shell", "/usr/sbin/nologin", "tijori"); err != nil {
			return err
		}
		fmt.Println("Created system user 'tijori'.")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	if err := run("chown", "-R", "tijori:tijori", cfg.DataDir); err != nil {
		return err
	}
	if err := run("chown", "root:tijori", cfgPath); err != nil {
		return err
	}
	_ = os.Chmod(cfgPath, 0o640)

	writable := cfg.DataDir
	if to := cfg.Backup.To; to != "" && !strings.Contains(to, "://") {
		_ = os.MkdirAll(to, 0o750)
		_ = run("chown", "tijori:tijori", to)
		writable += " " + to
	}
	caps := ""
	if _, port, err := net.SplitHostPort(cfg.Listen); err == nil {
		if p, _ := strconv.Atoi(port); p > 0 && p < 1024 {
			caps = "AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE"
		}
	}
	unit := fmt.Sprintf(systemdUnit, exe, cfgPath, caps, writable)
	if err := os.WriteFile("/etc/systemd/system/tijori.service", []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "--now", "tijori"); err != nil {
		return err
	}
	fmt.Println("Tijori is installed as a service and running.")
	fmt.Println("  Status:  systemctl status tijori")
	fmt.Println("  Logs:    journalctl -u tijori -f")
	fmt.Println("  Check:   curl " + localURL(cfg) + "/health")
	return nil
}

func uninstallSystemd() error {
	if os.Geteuid() != 0 {
		return errors.New("run this with sudo: sudo tijori service uninstall")
	}
	_ = run("systemctl", "disable", "--now", "tijori")
	if err := os.Remove("/etc/systemd/system/tijori.service"); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = run("systemctl", "daemon-reload")
	fmt.Println("Service removed. Your data and config were not touched.")
	return nil
}

func launchdPaths() (plist, logFile string) {
	if os.Geteuid() == 0 {
		return "/Library/LaunchDaemons/dev.tijori.plist", "/var/log/tijori.log"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "dev.tijori.plist"), filepath.Join(home, "Library", "Logs", "tijori.log")
}

func installLaunchd(cfgPath string) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if cfg.File == "" {
		return errors.New("no config file found; run `tijori init` first")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	plist, logFile := launchdPaths()
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(launchdPlist, exe, cfg.File, logFile, logFile)
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		return err
	}
	_ = run("launchctl", "unload", plist)
	if err := run("launchctl", "load", "-w", plist); err != nil {
		return err
	}
	fmt.Println("Tijori will now start automatically.")
	fmt.Println("  Logs:   tail -f " + logFile)
	fmt.Println("  Check:  curl " + localURL(cfg) + "/health")
	return nil
}

func uninstallLaunchd() error {
	plist, _ := launchdPaths()
	_ = run("launchctl", "unload", "-w", plist)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("Service removed. Your data and config were not touched.")
	return nil
}
