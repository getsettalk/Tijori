# Quotas

**You will:** limit how much space each bucket, each file and the whole server may use.

## Settings

```toml
max_total = "100GB"          # all buckets together
max_object_size = "5GB"      # largest single file anywhere (default 5GB)

[buckets.photos]
max_size = "10GB"            # this bucket
max_object_size = "20MB"     # largest file in this bucket
```

Sizes accept `KB`, `MB`, `GB` and `TB` (1 GB = 1024 MB). You can also set
bucket quotas in the admin panel or with
`tijori bucket create photos --max-size 10GB --max-object-size 20MB`.

## What happens when a limit is reached

The upload is refused **before anything is written** with HTTP 403 and a clear message:

```xml
<Code>QuotaExceeded</Code>
<Message>Bucket "photos" is full: quota 10 GB, used 9.9 GB, this upload needs 200 MB more.</Message>
```

A file over `max_object_size` gets `EntityTooLarge` instead. Replacing a file
only counts the difference in size.

## Seeing usage

- Admin panel: dashboard and **Buckets** page show bars.
- Terminal: `tijori usage`
- Prometheus: set `metrics = true` and read `tijori_bucket_bytes` from `/metrics`.

Usage is counted on every upload and delete and recounted from disk at startup.
If you changed files on disk by hand, run `tijori usage --recalc`.
