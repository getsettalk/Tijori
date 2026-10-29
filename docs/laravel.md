# Connect Laravel

**You will:** store Laravel uploads in Tijori using the normal `s3` disk.

## 1. Install the S3 driver

```sh
composer require league/flysystem-aws-s3-v3 "^3.0" --with-all-dependencies
```

## 2. Create a bucket and a key

In the admin panel create a bucket (for example `my-app`), then go to
**Keys → New key**, pick that bucket and **Read and write**. The dialog shows a
ready-to-paste `.env` block.

## 3. Edit `.env`

```env
FILESYSTEM_DISK=s3
AWS_ACCESS_KEY_ID=tj_XXXXXXXXXXXXXXXX
AWS_SECRET_ACCESS_KEY=your-secret
AWS_DEFAULT_REGION=auto
AWS_BUCKET=my-app
AWS_ENDPOINT=https://files.example.com
AWS_USE_PATH_STYLE_ENDPOINT=true
# Only for public buckets, so Storage::url() builds correct links:
AWS_URL=https://files.example.com/my-app
```

The default `s3` disk in `config/filesystems.php` already reads these variables.

## 4. Use it

```php
use Illuminate\Support\Facades\Storage;

// Upload from a form
$path = $request->file('avatar')->store('avatars');

// Write and read
Storage::put('notes/hello.txt', 'Hello from Laravel');
$text = Storage::get('notes/hello.txt');

// A link that works for 10 minutes (private buckets)
$url = Storage::temporaryUrl($path, now()->addMinutes(10));

// A permanent link (public buckets only, needs AWS_URL)
$url = Storage::url($path);

// Let a browser or phone upload directly (Laravel 10+)
['url' => $uploadUrl, 'headers' => $headers] =
    Storage::temporaryUploadUrl('uploads/photo.jpg', now()->addMinutes(5));
```

## Check it worked

```sh
php artisan tinker
>>> Storage::put('test.txt', 'ok'); Storage::exists('test.txt');
=> true
```

The file appears in the admin panel's file browser.

## Notes

- **Visibility:** Tijori decides public access per bucket (the **Public** switch),
  not per file. `Storage::setVisibility()` is accepted but does not change anything.
- **Resized images:** build URLs like `https://files.example.com/img/my-app/avatars/a.jpg?w=200`.
  See [Image resizing](images.md).
