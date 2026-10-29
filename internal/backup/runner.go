package backup

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

// Runner runs backups for the server: on a schedule and on demand from the
// admin panel. Only one backup runs at a time.
type Runner struct {
	Cfg   *config.Config
	Store *storage.Store
	DB    *state.DB
	Log   *slog.Logger

	mu      sync.Mutex
	running bool
}

// ErrRunning is returned when a backup is already in progress.
var ErrRunning = errors.New("a backup is already running")

// Running reports whether a backup is in progress.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// RunNow performs a backup and records its status.
func (r *Runner) RunNow() (state.BackupStatus, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return state.BackupStatus{}, ErrRunning
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	t, err := Open(r.Cfg.Backup.To)
	if err != nil {
		st := state.BackupStatus{Started: time.Now().UTC(), Finished: time.Now().UTC(), Dest: r.Cfg.Redacted().Backup.To, Error: err.Error()}
		_ = r.DB.SetLastBackup(st)
		return st, err
	}
	r.Log.Info("backup started", "to", t.String())
	st, err := Run(r.Store, r.DB, t, Options{ConfigFile: r.Cfg.File, Keep: r.Cfg.Backup.Keep,
		Progress: func(f string, a ...any) { r.Log.Info("backup progress", "detail", sprintf(f, a...)) }})
	if err != nil {
		r.Log.Error("backup failed", "to", t.String(), "err", err)
	} else {
		r.Log.Info("backup finished", "to", t.String(), "copied", st.Copied, "unchanged", st.Skipped, "bytes", st.Bytes,
			"took", st.Finished.Sub(st.Started).Round(time.Second))
	}
	_ = r.DB.SetLastBackup(st)
	return st, err
}

// Start runs a backup in the background.
func (r *Runner) Start() error {
	if r.Cfg.Backup.To == "" {
		return errors.New("no backup destination configured")
	}
	if r.Running() {
		return ErrRunning
	}
	go func() { _, _ = r.RunNow() }()
	return nil
}

// Schedule runs backups at the configured times until stop is closed.
func (r *Runner) Schedule(stop <-chan struct{}) {
	if r.Cfg.Backup.To == "" || r.Cfg.Backup.Schedule == "" {
		return
	}
	sched, err := config.ParseSchedule(r.Cfg.Backup.Schedule)
	if err != nil {
		r.Log.Error("invalid backup schedule", "err", err)
		return
	}
	for {
		next := sched.Next(time.Now())
		r.Log.Info("next scheduled backup", "at", next.Format(time.RFC3339))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			_, _ = r.RunNow()
		}
	}
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
