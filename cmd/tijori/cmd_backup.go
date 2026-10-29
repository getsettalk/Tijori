package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/getsettalk/Tijori/internal/backup"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/version"
)

func progress(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }

func cmdBackup(args []string) error {
	fs, cfgPath := newFlags("backup", "backup [--to DEST] [--keep 7]")
	to := fs.String("to", "", "destination: a folder, sftp://user@host/path or s3://bucket?endpoint=https://...")
	keep := fs.Int("keep", 0, "snapshots to keep (default from config, or 7)")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, db, store, err := openOffline(*cfgPath)
	if err != nil {
		return err
	}
	dest := *to
	if dest == "" {
		dest = cfg.Backup.To
	}
	if dest == "" {
		return errors.New("where should the backup go? Use --to /path/to/folder, or set [backup] to = ... in tijori.toml")
	}
	if *keep == 0 {
		*keep = cfg.Backup.Keep
	}
	t, err := backup.Open(dest)
	if err != nil {
		return err
	}
	fmt.Printf("Backing up %s to %s ...\n", cfg.DataDir, t)
	st, err := backup.Run(store, db, t, backup.Options{ConfigFile: cfg.File, Keep: *keep, Progress: progress})
	_ = db.SetLastBackup(st)
	if err != nil {
		return err
	}
	fmt.Printf("Done in %s: %d files copied (%s), %d unchanged.\n", st.Finished.Sub(st.Started).Round(time.Millisecond),
		st.Copied, config.FormatBytes(st.Bytes), st.Skipped)
	fmt.Printf("Restore later with: tijori restore --from %s\n", dest)
	return nil
}

// serverRunning reports whether something answers on the configured port.
func serverRunning(cfg *config.Config) bool {
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

func cmdRestore(args []string) error {
	fs, cfgPath := newFlags("restore", "restore --from SRC [--snapshot NAME] [--yes]")
	from := fs.String("from", "", "backup location (same format as backup --to)")
	snapshot := fs.String("snapshot", "", "snapshot of keys to restore (default: newest)")
	yes := fs.Bool("yes", false, "restore even if the server seems to be running")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *from == "" {
		*from = cfg.Backup.To
	}
	if *from == "" {
		return errors.New("use --from with the backup location")
	}
	if serverRunning(cfg) && !*yes {
		return fmt.Errorf("something is listening on %s; stop Tijori before restoring (or add --yes)", cfg.Listen)
	}
	t, err := backup.Open(*from)
	if err != nil {
		return err
	}
	fmt.Printf("Restoring from %s into %s ...\n", t, cfg.DataDir)
	res, err := backup.Restore(t, cfg.DataDir, *snapshot, progress)
	if err != nil {
		return err
	}
	fmt.Printf("Done: %d files restored (%s), %d already up to date.\n", res.Files, config.FormatBytes(res.Bytes), res.Skipped)
	if res.Snapshot != "" {
		fmt.Printf("Keys and admin users restored from snapshot %s.\n", res.Snapshot)
	}
	if res.ConfigFile != "" {
		fmt.Printf("The backed-up config was saved as %s; compare it with your tijori.toml.\n", res.ConfigFile)
	}
	fmt.Println("Start the server with: tijori serve")
	return nil
}

func cmdExport(args []string) error {
	fs, cfgPath := newFlags("export", "export [--out tijori-backup.tar.gz]")
	out := fs.String("out", "", "archive to write (default tijori-backup-DATE.tar.gz, or - for stdout)")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *out == "" {
		*out = "tijori-backup-" + time.Now().Format("2006-01-02") + ".tar.gz"
	}
	w := os.Stdout
	if *out != "-" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	info, err := backup.Export(w, cfg.DataDir, cfg.File, version.Version)
	if err != nil {
		if *out != "-" {
			os.Remove(*out)
		}
		return err
	}
	if *out != "-" {
		abs, _ := filepath.Abs(*out)
		fmt.Printf("Exported %d files (%s) to %s\n", info.Files, config.FormatBytes(info.Bytes), abs)
		fmt.Printf("The archive contains your keys; keep it private.\nOn the new server run: tijori import %s\n", filepath.Base(*out))
	}
	return nil
}

func cmdImport(args []string) error {
	fs, cfgPath := newFlags("import", "import ARCHIVE [--data-dir DIR] [--force]")
	dataDir := fs.String("data-dir", "", "where to unpack (default: data_dir from config)")
	force := fs.Bool("force", false, "overwrite a data folder that already has buckets")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: tijori import tijori-backup.tar.gz")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *dataDir == "" {
		*dataDir = cfg.DataDir
	}
	if serverRunning(cfg) && !*force {
		return fmt.Errorf("something is listening on %s; stop Tijori before importing", cfg.Listen)
	}
	f, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	configDest := cfg.File
	if configDest == "" {
		configDest, _ = defaultPaths()
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return err
	}
	res, err := backup.Import(f, *dataDir, configDest, *force)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %d files (%s) into %s\n", res.Files, config.FormatBytes(res.Bytes), *dataDir)
	if res.ConfigFile != "" {
		fmt.Printf("Config from the archive written to %s", res.ConfigFile)
		if filepath.Ext(res.ConfigFile) == ".imported" {
			fmt.Print(" (your existing config was kept; compare the two)")
		}
		fmt.Println(" (data_dir updated to " + *dataDir + ").")
	}
	fmt.Println("Start the server with: tijori serve")
	return nil
}
