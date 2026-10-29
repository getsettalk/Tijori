# Image resizing

**You will:** get smaller versions of your images just by changing the URL.

```text
https://files.example.com/img/<bucket>/<key>?w=300
```

The original is never changed. Results are cached on disk, so the second
request is instant (the response header `X-Tijori-Cache` says `HIT`).

## Options

| Parameter | Meaning | Example |
| --- | --- | --- |
| `w` | width in pixels | `w=300` |
| `h` | height in pixels | `h=200` |
| `fit` | `contain` (default): fit inside the box. `cover`: fill the box and crop the edges | `fit=cover` |
| `fmt` | `jpeg` or `png` (default: same as the original) | `fmt=jpeg` |
| `q` | JPEG quality 1-100 (default 80) | `q=70` |

Examples:

```text
/img/photos/beach.jpg?w=800                  # 800 px wide
/img/photos/beach.jpg?w=200&h=200&fit=cover  # square thumbnail
/img/photos/logo.png?w=64&fmt=png            # small PNG
```

Images are never made bigger than the original. Photos from phones are
rotated the right way automatically. Inputs can be JPEG, PNG, GIF or WebP.

!!! note "WebP output"
    Tijori is built without C libraries, so it can read WebP but not write it.
    `fmt=webp` returns JPEG (or PNG when the image has transparency) and adds
    an `X-Tijori-Note` header.

## Who can see resized images?

- **Public buckets:** anyone, like the originals.
- **Private buckets:** use a presigned URL for the `/img/...` address.

## Settings

```toml
[images]
enabled = true
max_width = 4000            # largest w allowed
max_height = 4000
max_pixels = 50000000       # refuse bigger originals (protects memory)
max_input_size = "50MB"
quality = 80
cache_size = "500MB"        # oldest unused entries are removed first
allowed_widths = [150, 300, 600, 1200]   # optional: stops people filling the cache
```

## Caching

Responses for public buckets send `Cache-Control: public, max-age=31536000`.
If you replace an image under the same name, browsers may keep the old one.
Add a version to the URL, for example `?w=300&v=2`, or upload under a new name.
