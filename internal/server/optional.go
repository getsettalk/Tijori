package server

import (
	"path/filepath"
	"time"

	"github.com/getsettalk/Tijori/internal/admin"
	"github.com/getsettalk/Tijori/internal/backup"
	"github.com/getsettalk/Tijori/internal/imageproc"
	"github.com/getsettalk/Tijori/internal/video"
)

// initOptional builds optional components (images, admin, video, backup).
func (s *Server) initOptional() error {
	if s.Cfg.Images.Enabled {
		ic := s.Cfg.Images
		cache, err := imageproc.NewCache(filepath.Join(s.Cfg.DataDir, ".cache", "images"), int64(ic.CacheSize))
		if err != nil {
			return err
		}
		s.ImageCache = cache
		s.Images = &imageproc.Handler{
			Store: s.Store, Verifier: s.S3.Verifier, Quota: s.Quota, Cache: cache, Log: s.Log,
			Limits: imageproc.Limits{
				MaxWidth: ic.MaxWidth, MaxHeight: ic.MaxHeight, MaxPixels: ic.MaxPixels,
				MaxInputSize: int64(ic.MaxInputSize), AllowedWidths: ic.AllowedWidths, Quality: ic.Quality,
			},
		}
	}
	s.Backups = &backup.Runner{Cfg: s.Cfg, Store: s.Store, DB: s.DB, Log: s.Log}
	s.Video = &video.Manager{
		Cfg: s.Cfg, Store: s.Store, Log: s.Log, Put: s.S3.Put,
		Enabled: func(bucket string) bool { return s.Quota.Settings(bucket).VideoProcess },
	}
	s.Video.Init()
	s.S3.Events = s.Video
	if s.Cfg.Admin.Enabled {
		a := &admin.Handler{Cfg: s.Cfg, DB: s.DB, Store: s.Store, Quota: s.Quota, S3: s.S3, Log: s.Log, Started: time.Now()}
		if ih, ok := s.Images.(*imageproc.Handler); ok {
			a.Images = ih
			a.ImageStats = s.ImageCache.Stats
		}
		a.Video = func() admin.VideoInfo {
			ok, path, ver, q, done, failed := s.Video.Info()
			return admin.VideoInfo{Available: ok, FFmpeg: path, Version: ver, Queued: q, Done: done, Failed: failed}
		}
		a.RunBackup = s.Backups.Start
		a.BackupRunning = s.Backups.Running
		s.AdminPanel = a
		s.Admin = a
	}
	return nil
}

// startOptional starts background work for optional components.
func (s *Server) startOptional() {
	s.Video.Start()
	s.closers = append(s.closers, s.Video.Stop)
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.Backups.Schedule(s.stop)
	}()
}
