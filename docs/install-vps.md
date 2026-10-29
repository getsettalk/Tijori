# Install on a VPS

**You will:** run Tijori on a Linux server (Ubuntu, Debian and others) as a
service that starts on boot.

You need a server with SSH access and `sudo`.

## 1. Install the binary

```sh
curl -fsSL https://raw.githubusercontent.com/getsettalk/Tijori/main/scripts/install.sh | sudo sh
```

The installer checks the download's SHA256 checksum and puts `tijori` in `/usr/local/bin`.

## 2. Create the config

```sh
sudo tijori init --public-url https://files.example.com
```

As root on Linux, this creates:

- `/etc/tijori/tijori.toml`: the settings
- `/var/lib/tijori`: where files are stored

Save the admin password and the first access key it prints. If you do not
have a domain yet, leave out `--public-url` and add it later.

## 3. Start it as a service

```sh
sudo tijori service install
```

This creates a `tijori` system user, gives it the data folder, and starts a
systemd service with security hardening.

## 4. Check it worked

```sh
systemctl status tijori
curl http://localhost:9000/health
sudo tijori doctor --config /etc/tijori/tijori.toml
```

## 5. Open it to the world safely

Do not expose port 9000 directly. Put a web server with HTTPS in front:
see [HTTPS with Caddy or Nginx](https.md). With a firewall such as `ufw`:

```sh
sudo ufw allow 80,443/tcp
```

## Everyday commands

| Task | Command |
| --- | --- |
| See logs | `journalctl -u tijori -f` |
| Restart after editing the config | `sudo systemctl restart tijori` |
| Create a key | `sudo tijori key create --name my-app --config /etc/tijori/tijori.toml` |
| Upgrade | run the install command again, then `sudo systemctl restart tijori` |
| Remove the service | `sudo tijori service uninstall` (data stays) |

Tip: `tijori` reads `/etc/tijori/tijori.toml` automatically when there is no
`tijori.toml` in the current folder, so `--config` is usually optional.
