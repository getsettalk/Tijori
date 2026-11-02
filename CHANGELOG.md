# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Admin panel: **Image URL** button for images in the file browser (and in the preview). Pick width,
  height, fit, what to keep and format, see a live preview with format, pixel size and file size,
  and copy the `/img/` link. Private buckets get a signed link. The admin API's presign endpoint
  accepts `img` and `image` for this.
- Admin panel: every folder shows what it holds, subfolders included
  (`2 folders · 128 files · 45.2 MB · updated 2 days ago`), and the line under the breadcrumb shows
  the totals of the current folder or bucket. Counting stops at 200,000 entries per folder
  (`200,000+ files`) so huge folders stay fast.

## [1.1.0] - 2026-11-02

### Added
- **Images come back in the format you uploaded.** WebP stays WebP (lossless stays lossless), GIF
  stays GIF, and animated GIF and WebP files stay animated. `fmt=webp` now really writes WebP (from
  JPEG, PNG or GIF), and `fmt=auto` sends WebP to browsers that accept it (with `Vary: Accept`). GIF
  and WebP animations can be converted into each other.
- **Proper cropping:**
  - `pos` keeps a side or corner with `fit=cover` (`top`, `bottom-left`…).
  - `pos=smart` finds the subject by colour contrast, detail and skin tones, ignoring sky, walls,
    floors and horizons, and avoids cutting objects in half.
  - `fp=x,y` sets a focal point.
  - `crop=x,y,w,h` cuts an exact rectangle first.
- New fit modes: `fit=pad` (whole image plus bars, in `bg` colour or transparent) and `fit=fill`
  (stretch to the exact size).
- `dpr` for Retina screens, `rot` and `flip`, `lossless=1` for WebP, `bg` for pads and JPEG
  backdrops, and `anim=0` for a still frame.
- Admin panel file thumbnails use the smart crop and show the first frame of animations.

### Changed
- The WebP codec is libwebp translated to pure Go: still one static binary with no C libraries, on
  every platform. Release builds use the `nodynamic` build tag so a system libwebp is never used.
  Binaries are about 3 MB larger.
- Animations are resized only up to 25 million pixels in total (frames × size); larger ones return
  their first frame with an `X-Tijori-Note` header.
- Resized images are rendered again after upgrades that change processing (the cache key has a
  render version).
- Backup page: on a plain HTTP address, a note explains that the browser may ask to confirm the
  download and that the file travels unencrypted. The password dialog includes the user name for
  password managers.

## [1.0.2] - 2026-10-29

### Added
- **Backup → Download backup** in the admin panel: one `.tar.gz` file with every bucket, file,
  access key and setting, saved by the browser straight to disk (any size). It asks for the
  password again, and the link works once, for two minutes.
- **Backup → Restore from a backup file**: upload such a file (or one from `tijori export`) to
  the same or another server while it runs. The upload goes in parts that fit proxy time limits;
  the whole file is checked (damaged, cut-off or unsafe files are refused) before anything
  changes. Buckets, files, metadata, bucket settings, keys and users are added; nothing on the
  server is deleted; existing admins keep their passwords. Option to keep or replace files that
  exist on both. Downloads and restores are logged with user and address.

### Fixed
- Backup page: the schedule shows "Not scheduled" instead of "– · keep 7" when none is set.
- Admin panel uploads: when a part fails because the connection got slower or a proxy cut it
  (504, 499, dropped connection), the retry sends a part half the size instead of the same one,
  so uploads keep going on connections whose speed changes.

## [1.0.1] - 2026-10-29

### Security
- Release binaries are now built with the latest patched Go (`toolchain go1.27.3`). v1.0.0 was
  built with Go 1.26.0, which has known vulnerabilities in `net/http`, `net/textproto`,
  `crypto/tls` and `crypto/x509`. **Upgrade from 1.0.0.**
- Admin login rate limiting uses the real client address behind reverse proxies (Coolify, Nginx,
  Caddy), so one client's failed logins no longer lock everyone out; its memory use is bounded.
- On Windows, keys that map to device names or alternate data streams are refused.
- Presigned and signed requests are refused when they carry unsigned `x-amz-*` headers (S3's
  HeadersNotSigned rule). Before, a presigned upload URL could be turned into a copy of any object
  its key could read.
- ffmpeg only opens local files in real video containers, so a playlist cannot read other buckets.
- `.tijori`-reserved names are checked case-insensitively (macOS/Windows disks).
- Quotas count unfinished multipart uploads; at most 1000 unfinished uploads per bucket.
- DeleteObjects and CompleteMultipartUpload verify the payload hash before acting.
- aws-chunked trailers are size-limited; import/restore reject backslash paths and invalid bucket
  names; the SSH control socket lives in a private directory.
- Login limiter: IPv6 /64 grouping, per-user counters only for existing users, bounded memory;
  `trust_proxy_headers` setting.

### Added
- Opening the server address in a browser redirects to the admin panel instead of showing an S3
  `AccessDenied` XML (S3 clients are unaffected).
- `cors_origins` for browser uploads with presigned URLs.
- The admin panel uploads big files (8 MB and up) in parts of about 15 seconds each, with retries,
  so uploads work behind proxies that cut long requests (Coolify's Traefik stops at 60 s) and on
  slow connections. Clearer error messages for proxy timeouts.
- The admin panel shows which build is running (version, commit, build date) on every page, and
  says **"Update available"** when a newer release is out, with a link to the release notes. The
  server asks GitHub's API once every 12 hours, anonymously; `check_updates = false` turns it off.
  Binaries built with plain `go build` from a git checkout now report their version and commit too.
- After an upgrade, browsers load the new admin panel at once (asset links carry a content hash).
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
