# Coolify

**You will:** deploy Tijori on [Coolify](https://coolify.io) with HTTPS on your own domain.

## Steps

1. In Coolify, open your project and choose **New Resource → Docker Compose**.
2. Paste the contents of
   [deploy/coolify/docker-compose.yml](https://github.com/getsettalk/Tijori/blob/main/deploy/coolify/docker-compose.yml).
3. Coolify shows a **Domains** field for the `tijori` service. Enter your
   domain, for example `https://files.example.com`. Point that domain's DNS at your Coolify server.
4. Open **Environment Variables**. Coolify fills in `SERVICE_PASSWORD_ADMIN`
   for you; copy it, because that is your admin password. Optionally set
   `TIJORI_ACCESS_KEY` and `TIJORI_SECRET_KEY` to give your app a fixed key.
5. Click **Deploy**.

Coolify's proxy handles HTTPS. Tijori's health check tells Coolify when it is ready.

## Check it worked

- `https://files.example.com/health` shows `{"status":"ok"}`.
- `https://files.example.com/_admin/` shows the login page.

## Your data

Files live in the Docker volume `tijori-data`. To back up or move:

- **From inside the container:** in Coolify's terminal for the service, run
  `tijori export --out /data/export.tar.gz`, then download the file.
- **Copy the volume:** on the server, `docker volume inspect` shows its folder;
  copy that folder with `rsync -a`.
- **Scheduled backups:** set `TIJORI_BACKUP_TO`, `TIJORI_BACKUP_SCHEDULE=daily 03:00`
  and, for S3 destinations, `TIJORI_BACKUP_ACCESS_KEY` / `TIJORI_BACKUP_SECRET_KEY`.

See [Backup, restore and moving servers](backup.md).

## Video processing

Change the image to `ghcr.io/getsettalk/tijori:latest-ffmpeg` and redeploy.
