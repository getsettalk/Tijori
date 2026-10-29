# CLI reference

Every command accepts `--config PATH`. Without it Tijori uses `./tijori.toml`,
then `/etc/tijori/tijori.toml`. Run `tijori <command> -h` for all flags.

| Command | What it does |
| --- | --- |
| `tijori` or `tijori serve` | Start the server. |
| `tijori init` | Create the config, data folder, admin password and first key. Flags: `--data-dir`, `--listen`, `--public-url`, `--admin-password`, `--yes` (no questions). Safe to run again. |
| `tijori key create` | New key. `--name laravel --buckets photos,docs --permission readwrite\|read\|write`. Prints the secret once. |
| `tijori key list` | List keys (never shows secrets). |
| `tijori key revoke ACCESS_KEY` | Delete a key; apps using it get `AccessDenied`. |
| `tijori bucket create NAME` | `--public`, `--max-size 10GB`, `--max-object-size 500MB`. |
| `tijori bucket list` | Buckets with file counts, size, quota, public flag. |
| `tijori bucket delete NAME` | Only if empty; `--force` deletes the files too. |
| `tijori usage [--recalc]` | Space used per bucket. `--recalc` recounts from disk. |
| `tijori admin password` | Reset a password. `--user NAME`, `--password PW`, `--readonly` for a view-only user. |
| `tijori backup --to DEST` | Back up now (default destination from `[backup] to`). `--keep N`. |
| `tijori restore --from SRC` | Restore a backup into the data folder. Stop the server first. `--snapshot NAME`. |
| `tijori export --out FILE` | Pack data, keys and config into one `.tar.gz` (`--out -` for stdout). |
| `tijori import FILE` | Unpack an export. Refuses to overwrite buckets unless `--force`. |
| `tijori service install` | Linux: systemd service with a `tijori` user. macOS: launchd agent. |
| `tijori service uninstall` | Remove the service (data stays). |
| `tijori doctor` | Check config, permissions, disk space, ffmpeg, port and keys. |
| `tijori version` | Version, commit and build date. |
| `tijori healthcheck` | Exit 0 when the local server answers `/health` (used by Docker). |

Commands that change keys or buckets work while the server is running; the
server picks up the change within a couple of seconds.
