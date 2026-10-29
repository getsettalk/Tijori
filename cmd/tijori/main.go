// Command tijori is a tiny, self-hosted, S3-compatible object storage server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/server"
	"github.com/getsettalk/Tijori/internal/version"
)

const usage = `Tijori - a tiny S3-compatible storage server.

Usage:
  tijori [command] [flags]

Commands:
  serve              Start the server (default when no command is given)
  init               Create config, data folder, admin password and first key
  key create|list|revoke
  bucket create|list|delete
  usage [--recalc]   Show storage used per bucket
  admin password     Reset an admin panel password
  backup --to DEST   Copy all data to a folder, sftp:// or s3:// destination
  restore --from SRC Restore data from a backup
  export --out FILE  Pack data, config and keys into one .tar.gz
  import FILE        Unpack an export into the data folder
  service install|uninstall   Run Tijori as a system service
  doctor             Check permissions, disk space, ffmpeg, port and config
  version            Print version

Every command accepts --config PATH (default: ./tijori.toml, then
/etc/tijori/tijori.toml). Run "tijori <command> -h" for command flags.
Docs: https://github.com/getsettalk/Tijori
`

type command struct {
	run  func(args []string) error
	help string
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"serve":       {cmdServe, "Start the server"},
		"init":        {cmdInit, "Set up Tijori"},
		"key":         {cmdKey, "Manage access keys"},
		"keys":        {cmdKey, "Manage access keys"},
		"bucket":      {cmdBucket, "Manage buckets"},
		"buckets":     {cmdBucket, "Manage buckets"},
		"usage":       {cmdUsage, "Show usage"},
		"admin":       {cmdAdmin, "Admin users"},
		"backup":      {cmdBackup, "Back up data"},
		"restore":     {cmdRestore, "Restore data"},
		"export":      {cmdExport, "Export everything"},
		"import":      {cmdImport, "Import an export"},
		"service":     {cmdService, "System service"},
		"doctor":      {cmdDoctor, "Check setup"},
		"healthcheck": {cmdHealthcheck, "Exit 0 if the local server is healthy (for Docker)"},
		"version":     {cmdVersion, "Print version"},
	}
}

func main() {
	args := os.Args[1:]
	name := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	switch name {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	case "--version":
		name = "version"
	}
	if len(args) > 0 && (args[0] == "--version") {
		name = "version"
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown command %q.\n\n%s", name, usage)
		os.Exit(2)
	}
	if err := cmd.run(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// newFlags creates a flag set with the shared --config flag.
func newFlags(name, synopsis string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to tijori.toml")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tijori %s\n\nFlags:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs, cfgPath
}

// parseFlags parses flags that may appear before or after positional args.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path)
}

func cmdServe(args []string) error {
	fs, cfgPath := newFlags("serve", "serve [--config tijori.toml]")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	log := server.NewLogger(cfg, os.Stderr)
	srv, err := server.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Run(ctx)
}

func cmdVersion(args []string) error {
	fmt.Println(version.String())
	return nil
}
