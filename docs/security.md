# Security notes

**You will:** know the defaults that protect you and the few things you should do yourself.

## Do this

1. **Use HTTPS** on any network you do not fully trust. Tijori warns at startup
   if `public_url` is plain `http` for a non-local address. See [HTTPS](https.md).
2. **One key per app**, limited to the buckets it needs and to `read` or
   `write` when it does not need both. Revoke keys you no longer use.
3. **Never put secret keys in mobile or browser code.** Use presigned URLs ([Mobile apps](mobile.md)).
4. **Protect the admin panel:** a long password, and if possible serve it on
   `127.0.0.1` with `[admin] listen` and use an SSH tunnel, or disable it.
5. **Back up** `data_dir`, `tijori.db.json` and `tijori.toml`, and test a restore once.
6. **Keep Tijori updated.**

## What Tijori does for you

- AWS Signature V4 on every request, compared in constant time; 15 minutes of allowed clock skew; presigned URLs expire (7 days maximum).
- Object keys with `..`, `.`, empty parts, backslashes or NUL bytes are rejected, so no request can reach files outside the bucket folders.
- Quotas and size limits are checked before anything is written. Image processing refuses huge images to prevent memory exhaustion.
- `tijori.db.json` (keys and password hashes) and generated configs are written with `0600` permissions. Admin passwords use bcrypt.
- Admin panel: `HttpOnly` + `SameSite=Strict` session cookies (`Secure` behind HTTPS), CSRF tokens on every change, rate-limited logins, a strict Content-Security-Policy and clickjacking protection.
- Stored HTML, SVG and XML files are served with `Content-Security-Policy: sandbox`, so an uploaded web page cannot run scripts on your Tijori domain. All responses send `X-Content-Type-Options: nosniff`.
- Secrets and query strings (which hold presigned signatures) are never logged.
- Public buckets allow anonymous downloads only, never uploads, deletes or listing.
- The Docker images run as a non-root user; the systemd unit uses a dedicated user and sandboxing.
- Behind a reverse proxy (Caddy, Nginx, Coolify) the real client address is taken from the
  right-most `X-Forwarded-For` entry, and only when the direct peer is a private or loopback
  address. Clients cannot fake it, and one attacker's failed logins do not lock out everyone else.
- CORS is off unless you list origins in `cors_origins`. Allowing an origin never grants access by
  itself: requests still need a valid signature (or a public bucket).
- On Windows, keys that would map to device names (`CON`, `NUL`, `COM1`…) or alternate data streams
  (`a:b`) are refused.
- Releases are built with the latest patched Go, and every change is checked with `govulncheck`
  (known vulnerabilities), `staticcheck`, the race detector, a fuzz test for key paths, and tests on
  Linux, macOS and Windows. Each release is installed and exercised on five platforms before you
  see it.

## Things to know

- **Plain HTTP is for testing.** Coolify's free `sslip.io` address and `http://localhost` send
  keys and files unencrypted. Use your own domain with HTTPS for real data.
- **`/metrics`** (off by default) shows bucket names and sizes to anyone who can reach it. Keep it
  on a private network or behind your proxy's authentication.
- **Case-insensitive disks.** macOS and Windows treat `Photo.jpg` and `photo.jpg` as the same file,
  S3 does not. Run production on Linux (case-sensitive) if your keys differ only by case.
- **Rotate secrets you have shared.** If an admin password or a secret key appeared in a
  screenshot, a chat or a log, change it: `tijori admin password`, and revoke/recreate keys.

## Reporting a problem

See [SECURITY.md](https://github.com/getsettalk/Tijori/blob/main/SECURITY.md).
