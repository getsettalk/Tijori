# Configuration reference

Tijori reads `./tijori.toml`, then `/etc/tijori/tijori.toml`, or the file given
with `--config` / `TIJORI_CONFIG`. Without any file it runs on defaults.

**Every setting can also be set with an environment variable**: `TIJORI_` plus
the setting path in capitals with `_`. Environment variables win over the file.
Examples: `listen` → `TIJORI_LISTEN`, `[images] max_width` → `TIJORI_IMAGES_MAX_WIDTH`.
Lists are comma-separated: `TIJORI_IMAGES_ALLOWED_WIDTHS=150,300`.

Unknown settings in the file are reported as errors, so typos do not go unnoticed.
Sizes accept `KB`, `MB`, `GB`, `TB` (powers of 1024). `0` or empty means "no limit".

## Main

| Setting | Default | Meaning |
| --- | --- | --- |
| `listen` | `":9000"` | Address and port. Use `"127.0.0.1:9000"` behind a proxy. |
| `data_dir` | `"./data"` | Where files live. Relative paths are relative to the config file. |
| `public_url` | | Address users reach, e.g. `https://files.example.com`. Used for links. |
| `region` | `"auto"` | Region reported to clients. Clients may use any region when signing. |
| `max_total` | | Limit for all buckets together. |
| `max_object_size` | `"5GB"` | Largest single file. |
| `log_format` | `"text"` | `text` or `json`. |
| `log_level` | `"info"` | `debug`, `info`, `warn`, `error`. |
| `metrics` | `false` | Serve Prometheus metrics at `/metrics`. |
| `cors_origins` | `[]` | Web origins whose JavaScript may call Tijori (presigned uploads from a browser), e.g. `["https://app.example.com"]`. `["*"]` allows any site. Empty = off. Env: `TIJORI_CORS_ORIGINS=https://a.com,https://b.com`. |
| `trust_proxy_headers` | `true` | Behind a reverse proxy, take the real client address from `X-Forwarded-For` (only when the direct peer is a private or loopback address). Set `false` if Tijori is reached directly by clients on a private network. |
| `check_updates` | `true` | Show "Update available" in the admin panel when a newer release is out. The server sends one anonymous request to the GitHub API every 12 hours; no data about your server is sent. Set `false` on servers without internet access or if you prefer no outgoing requests. Env: `TIJORI_CHECK_UPDATES=false`. |
| `error_format` | `"auto"` | Format of error responses. `auto`: XML, but JSON when the request sends `Accept: application/json`. `xml`: always XML. `json`: always JSON (see below). |
| `access_key`, `secret_key` | | A root key with full access. Usually set as `TIJORI_ACCESS_KEY` / `TIJORI_SECRET_KEY`. |

### Errors in JSON

S3 clients expect errors as XML, so that is the default. Apps that call
Tijori directly (for example a phone uploading with a presigned URL) can ask
for JSON instead by sending `Accept: application/json`:

```json
{"error": {"code": "SignatureDoesNotMatch", "message": "The request signature we calculated does not match...",
           "status": 403, "bucket": "photos", "key": "a.jpg", "resource": "/photos/a.jpg", "request_id": "8F3A..."}}
```

Successful responses (file contents, bucket listings) do not change.

`error_format = "json"` sends JSON to everyone. Only use it if no S3 SDK talks
to this server: Laravel/Flysystem, the AWS SDKs and boto3 read XML errors and
would no longer recognise codes such as `NoSuchKey`. Tijori logs a warning at
startup in this mode.

## `[admin]`

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Turn the admin panel on or off. |
| `path` | `"/_admin"` | URL path of the panel. |
| `listen` | | Serve the panel on its own address only, e.g. `"127.0.0.1:9001"`. |
| `password` | | Sets the `admin` user's password at startup. Usually `TIJORI_ADMIN_PASSWORD`. |

## `[buckets.<name>]`

Buckets listed here are created at startup. Their settings can then only be changed in this file.

| Setting | Default | Meaning |
| --- | --- | --- |
| `public_read` | `false` | Anyone may download files (never upload). |
| `max_size` | | Bucket quota. |
| `max_object_size` | | Largest file in this bucket. |
| `video_process` | `false` | Make thumbnails and smaller copies of videos. |

Environment form: `TIJORI_BUCKETS_<NAME>_<SETTING>`, where `_` in the name
means `-`. Example: `TIJORI_BUCKETS_MY_PHOTOS_PUBLIC_READ=true` for bucket
`my-photos`. Shortcut: `TIJORI_PUBLIC_BUCKETS=photos,assets`.

## `[images]`

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Serve `/img/...`. |
| `max_width`, `max_height` | `4000` | Largest output size allowed. |
| `max_pixels` | `50000000` | Refuse originals with more pixels (protects memory). |
| `max_input_size` | `"50MB"` | Refuse larger originals. |
| `allowed_widths` | | If set, only these `w` values are allowed. |
| `quality` | `80` | Default JPEG quality. |
| `cache_size` | `"500MB"` | Disk space for resized images. |

## `[video]`

| Setting | Default | Meaning |
| --- | --- | --- |
| `ffmpeg` | | Path to ffmpeg. Empty = search `PATH`. |
| `workers` | `1` | Videos processed at the same time. |
| `presets` | `["thumbnail", "720p"]` | Any of `thumbnail`, `480p`, `720p`, `1080p`. |
| `webhook` | | URL to `POST` results to. |
| `webhook_secret` | | Key for the `X-Tijori-Signature` HMAC. |

## `[backup]`

| Setting | Default | Meaning |
| --- | --- | --- |
| `to` | | Folder, `sftp://user@host/path`, or `s3://bucket/prefix?endpoint=https://...` |
| `schedule` | | `daily 03:00`, `weekly sun 04:00`, `every 6h` or `hourly`. |
| `keep` | `7` | Snapshots to keep; deleted files stay this many backups. |

S3 backup credentials: `TIJORI_BACKUP_ACCESS_KEY` and `TIJORI_BACKUP_SECRET_KEY`
(or `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`). Add `&region=...` to the
`s3://` URL if your provider needs a specific region.

## Full example

```toml
listen   = "127.0.0.1:9000"
data_dir = "/var/lib/tijori"
public_url = "https://files.example.com"
max_total = "100GB"

[admin]
enabled = true
path = "/_admin"

[buckets.photos]
public_read = true
max_size = "10GB"
max_object_size = "20MB"

[buckets.videos]
video_process = true

[images]
max_width = 2000
cache_size = "500MB"

[video]
workers = 1
webhook = "https://yourapp.com/hooks/tijori"
webhook_secret = "change-me"

[backup]
to = "s3://my-backups?endpoint=https://s3.eu-central-003.backblazeb2.com"
schedule = "daily 03:00"
keep = 7
```

Check a config with `tijori doctor`. Restart Tijori after changing the file.
