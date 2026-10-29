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

## Reporting a problem

See [SECURITY.md](https://github.com/getsettalk/Tijori/blob/main/SECURITY.md).
