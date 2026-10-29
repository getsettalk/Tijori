# Docker and Docker Compose

**You will:** run Tijori in a container with your files in a Docker volume.

## Docker

```sh
docker run -d --name tijori --restart unless-stopped \
  -p 9000:9000 \
  -v tijori-data:/data \
  -e TIJORI_ADMIN_PASSWORD=change-me-please \
  ghcr.io/getsettalk/tijori:latest
```

Check it worked:

```sh
curl http://localhost:9000/health
docker ps   # STATUS should say "healthy" after a few seconds
```

Open <http://localhost:9000/_admin/>, log in as `admin` with your password,
then create a bucket and an access key.

No config file is needed. Every setting can be passed as an environment
variable (see [Configuration](configuration.md)).

### Give your app a fixed key

You can choose a key up front, so you can put it in your app's settings right away:

```sh
-e TIJORI_ACCESS_KEY=tj_MYAPPKEY0000001 \
-e TIJORI_SECRET_KEY=$(openssl rand -hex 20)
```

### Images

| Tag | Contents |
| --- | --- |
| `latest`, `1.0.0` | Small image (distroless), no ffmpeg |
| `latest-ffmpeg`, `1.0.0-ffmpeg` | Alpine with ffmpeg, for [video processing](video.md) |

Both run as a non-root user and include a health check.

## Docker Compose

Save [deploy/docker-compose.yml](https://github.com/getsettalk/Tijori/blob/main/deploy/docker-compose.yml)
and create a `.env` file next to it:

```env
TIJORI_ADMIN_PASSWORD=choose-a-long-password
TIJORI_PUBLIC_URL=https://files.example.com
```

Then:

```sh
docker compose up -d
docker compose logs -f
```

## Using a config file

Mount it and point Tijori at it:

```sh
-v ./tijori.toml:/etc/tijori/tijori.toml:ro -e TIJORI_CONFIG=/etc/tijori/tijori.toml
```

Keep `data_dir = "/data"` in that file.

## Running commands inside the container

```sh
docker exec tijori tijori key create --name laravel
docker exec tijori tijori usage
docker exec tijori tijori export --out /data/export.tar.gz
docker cp tijori:/data/export.tar.gz .
```
