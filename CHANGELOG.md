# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.1] - 2026-10-29

### Security
- Release binaries are now built with the latest patched Go (`toolchain go1.27.3`). v1.0.0 was
  built with Go 1.26.0, which has known vulnerabilities in `net/http`, `net/textproto`,
  `crypto/tls` and `crypto/x509`. **Upgrade from 1.0.0.**
- Admin login rate limiting uses the real client address behind reverse proxies (Coolify, Nginx,
  Caddy), so one client's failed logins no longer lock everyone out; its memory use is bounded.
- On Windows, keys that map to device names or alternate data streams are refused.

### Added
- `cors_origins` for browser uploads with presigned URLs.
- Coolify guide with real screenshots and a recorded walkthrough; "Build the binary yourself" guide.
- CI runs tests on Linux, macOS and Windows plus `govulncheck`; every release is installed and
  tested on Linux (amd64, arm64), macOS (Intel, Apple Silicon) and Windows, and both Docker images
  on amd64 and arm64.

### Fixed
- Admin: the new-key dialog opens at the top; the Laravel `.env` snippet uses a real bucket name.
- `sftp://` backups work on Windows (no SSH connection sharing there).
- Docker images report the version without a leading `v`.

## [1.0.0] - 2026-10-29

First release.

### Added
- S3 API: buckets, put/get/head/delete, ranges, copy, batch delete, ListObjects v1/v2, multipart uploads, presigned URLs, object ACL reads.
- Errors as XML (S3 default) or JSON: per request with `Accept: application/json`, or always with `error_format = "json"`.
- AWS Signature V4 for headers, presigned URLs and aws-chunked streaming uploads (signed and unsigned trailers).
- Access keys with per-bucket access and read/write permissions; public-read buckets.
- Per-bucket and global quotas with usage tracking.
- Image resizing route with disk cache and LRU eviction.
- Video thumbnails and 720p versions with ffmpeg, persisted job queue and signed webhooks.
- Embedded admin panel: dashboard, buckets, file browser, keys, share links, backups, settings; mobile and dark mode.
- Backup to a folder, SSH server or S3 service; restore; export/import archives; scheduled backups.
- CLI: serve, init, key, bucket, usage, admin password, backup, restore, export, import, service, doctor, version.
- Install script, Docker images (with and without ffmpeg), Compose and Coolify files, systemd unit.
