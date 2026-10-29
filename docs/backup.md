# Backup, restore and moving servers

**You will:** keep a copy of everything somewhere else, bring it back, and move Tijori to a new server.

A backup contains every bucket, every file with its metadata, your access
keys, admin users and config.

## Where backups can go

| Destination | Example |
| --- | --- |
| A folder (USB disk, NAS mount) | `/mnt/backup/tijori` |
| An SSH server | `sftp://backup@nas.example.com/srv/tijori` |
| Any S3 service | `s3://my-backups/tijori?endpoint=https://s3.eu-central-003.backblazeb2.com` |

For SSH, Tijori uses your normal `ssh` command, so set up key login first
(`ssh-copy-id backup@nas.example.com`). For S3 (Backblaze B2, Cloudflare R2,
AWS, or another Tijori), set the credentials as environment variables:

```sh
export TIJORI_BACKUP_ACCESS_KEY=...
export TIJORI_BACKUP_SECRET_KEY=...
```

## Back up now

```sh
tijori backup --to /mnt/backup/tijori
```

Only new or changed files are copied next time (Tijori compares size and checksum).

## Back up automatically

```toml
[backup]
to = "s3://my-backups/tijori?endpoint=https://<account>.r2.cloudflarestorage.com"
schedule = "daily 03:00"     # or "weekly sun 04:00", "every 6h", "hourly"
keep = 7
```

`keep` is how many snapshots of keys and config to keep. Files you delete
from Tijori stay in the backup for `keep` more backups before they are
removed, so a mistake can still be undone.

The **Backup** page in the admin panel shows the last result and has a
**Run backup now** button.

## Restore

Stop Tijori first, then:

```sh
sudo systemctl stop tijori
sudo tijori restore --from /mnt/backup/tijori
sudo chown -R tijori:tijori /var/lib/tijori
sudo systemctl start tijori
```

The backed-up config is saved as `tijori.restored.toml` in the data folder so
you can compare it with yours.

## Export and import (one file)

```sh
tijori export --out tijori-backup.tar.gz
tijori import tijori-backup.tar.gz
```

The archive holds data, config and keys. Keep it private.

## Move to a new server

1. **Install Tijori on the new server** ([Install on a VPS](install-vps.md)), but do not run `init`.
2. **Copy the data.** On the old server either:
    - `sudo tijori export --out tijori-backup.tar.gz`, copy the file across
      (`scp`), then on the new server `sudo tijori import tijori-backup.tar.gz`; or
    - `sudo rsync -a /var/lib/tijori/ new-server:/var/lib/tijori/` and copy
      `/etc/tijori/tijori.toml` too.
3. **Start it:** `sudo tijori service install`, then check
   `curl http://localhost:9000/health` and log in to the admin panel.
4. **Switch traffic:** point your DNS (or proxy) at the new server.

Your apps keep the same endpoint and keys, so nothing changes in Laravel or Node.js.

### On Coolify

Files live in the `tijori-data` Docker volume. Either run
`tijori export --out /data/export.tar.gz` in the service's terminal and
download the file, or copy the volume folder (`docker volume inspect` shows
where it is) with `rsync -a`. On the new Coolify server, deploy Tijori, then
`docker cp` the archive in and run `tijori import --force /data/export.tar.gz`.

## Other tools work too

Files are plain files under `data_dir`, so `restic`, `borg` or `rsync` backups
of that folder are fine. Back up `tijori.db.json` (keys) and `tijori.toml` as well.
