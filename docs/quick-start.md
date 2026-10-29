# Quick Start in 5 minutes

**You will:** install Tijori on your computer, start it, upload a file and see it in the admin panel.

## 1. Install

=== "Linux / macOS"

    ```sh
    curl -fsSL https://raw.githubusercontent.com/getsettalk/Tijori/main/scripts/install.sh | sh
    ```

=== "Windows"

    Download `tijori_<version>_windows_amd64.zip` from the
    [releases page](https://github.com/getsettalk/Tijori/releases), unzip it,
    and open a terminal in that folder. Use `tijori.exe` instead of `tijori` below.

=== "Docker"

    See [Docker and Compose](docker.md) and skip to step 4.

Check it worked:

```sh
tijori version
```

## 2. Set up

Make a folder for Tijori and run `init` inside it:

```sh
mkdir my-storage && cd my-storage
tijori init
```

It asks for an admin password (press Enter to get a random one) and prints
something like this:

```text
  Admin panel:   http://localhost:9000/_admin/
  Admin login:   admin / ********
  Access key:    tj_4K2M9QX7PL3VB8ZN
  Secret key:    p6Nq...40 characters...
```

**Copy the secret key now.** It is shown only once (you can always make new keys).

## 3. Start

```sh
tijori serve
```

Leave this terminal open. In another terminal, check it works:

```sh
curl http://localhost:9000/health
```

You should see `{"status":"ok"}`.

## 4. Use the admin panel

1. Open <http://localhost:9000/_admin/> and log in as `admin`.
2. Go to **Buckets** → **New bucket**, name it `my-files`, and create it.
3. Click **Browse**, then drag a photo onto the page to upload it.

## 5. Connect an app

Use these settings in any S3 library:

| Setting | Value |
| --- | --- |
| Endpoint | `http://localhost:9000` |
| Region | `auto` |
| Access key / Secret key | from step 2 |
| Path-style URLs | **on** (`use_path_style_endpoint` / `forcePathStyle`) |

Guides: [Laravel](laravel.md) · [Node.js](nodejs.md) · [Mobile apps](mobile.md).

## What next?

- Put Tijori on a server: [Install on a VPS](install-vps.md).
- Turn on HTTPS: [HTTPS with Caddy or Nginx](https.md).
- Set up backups: [Backup, restore and moving servers](backup.md).
