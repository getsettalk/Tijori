# Video processing

**You will:** get a thumbnail and a 720p copy of every video uploaded to a bucket.

## Requirements

[ffmpeg](https://ffmpeg.org/) must be installed (`apt install ffmpeg`,
`brew install ffmpeg`, or use the `latest-ffmpeg` Docker image). Without it the
feature turns itself off. The dashboard shows whether it was found.

## Turn it on for a bucket

In the admin panel edit the bucket and tick **Video processing**, or in `tijori.toml`:

```toml
[buckets.videos]
video_process = true
```

## What you get

Uploading `trips/goa.mp4` creates, in the same bucket:

| File | What |
| --- | --- |
| `trips/goa.jpg` | thumbnail from the first second |
| `trips/goa_720p.mp4` | H.264/AAC copy, at most 720 px high, ready to stream |

Choose other outputs with `presets` (available: `thumbnail`, `480p`, `720p`, `1080p`):

```toml
[video]
workers = 1                        # videos processed at the same time
presets = ["thumbnail", "480p", "720p"]
ffmpeg = ""                        # empty = find ffmpeg on PATH
```

Jobs are saved to disk, so a restart does not lose them.

## Webhook: know when it is done

```toml
[video]
webhook = "https://yourapp.com/hooks/tijori"
webhook_secret = "a-long-random-string"
```

Tijori sends a `POST` with JSON:

```json
{
  "event": "video.processed",
  "bucket": "videos",
  "key": "trips/goa.mp4",
  "outputs": [
    {"preset": "thumbnail", "key": "trips/goa.jpg", "size": 48211, "content_type": "image/jpeg"},
    {"preset": "720p", "key": "trips/goa_720p.mp4", "size": 3911877, "content_type": "video/mp4"}
  ],
  "error": "",
  "duration_ms": 5321
}
```

On failure `event` is `video.failed` and `error` explains why. Failed
deliveries are retried 3 times.

### Check the signature

The header `X-Tijori-Signature` is `sha256=` plus the HMAC-SHA256 of the raw body
with your `webhook_secret`.

=== "Laravel"

    ```php
    $expected = 'sha256='.hash_hmac('sha256', $request->getContent(), config('services.tijori.secret'));
    abort_unless(hash_equals($expected, $request->header('X-Tijori-Signature', '')), 401);
    ```

=== "Node.js (Express)"

    ```js
    app.post("/hooks/tijori", express.raw({ type: "application/json" }), (req, res) => {
      const expected = "sha256=" + crypto.createHmac("sha256", process.env.TIJORI_WEBHOOK_SECRET).update(req.body).digest("hex");
      const given = req.get("X-Tijori-Signature") || "";
      if (given.length !== expected.length || !crypto.timingSafeEqual(Buffer.from(given), Buffer.from(expected))) return res.sendStatus(401);
      const event = JSON.parse(req.body);
      res.sendStatus(200);
    });
    ```
