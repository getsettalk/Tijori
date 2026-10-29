# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report privately through GitHub: open the repository's **Security** tab and
choose **Report a vulnerability**. Include what you found, how to reproduce
it, and the Tijori version (`tijori version`).

We aim to reply within 3 working days and to release a fix for confirmed
issues within 30 days. We will credit you in the release notes unless you
prefer otherwise.

## Supported versions

Security fixes go into the latest release. Please upgrade before reporting.

## Hardening tips

See [docs/security.md](docs/security.md): use HTTPS, keep the admin panel on a
separate address or behind a VPN if you can, give each app its own key with
only the buckets it needs, and protect `tijori.toml` and `tijori.db.json`
(they hold secrets and are created with `0600` permissions).
