# HTTPS with Caddy or Nginx

**You will:** serve Tijori at `https://files.example.com` with a free certificate.

Always use HTTPS on the internet. Without it, files and keys travel in plain text.

Before you start, point your domain's DNS (an `A` record) at your server and
open ports 80 and 443.

## Option A: Caddy (simplest)

Caddy gets and renews certificates automatically.

1. [Install Caddy](https://caddyserver.com/docs/install).
2. Put this in `/etc/caddy/Caddyfile` (replace the domain):

    ```text
    files.example.com {
        reverse_proxy 127.0.0.1:9000 {
            flush_interval -1
        }
    }
    ```

3. `sudo systemctl reload caddy`

## Option B: Nginx

Use [deploy/nginx.conf](https://github.com/getsettalk/Tijori/blob/main/deploy/nginx.conf)
and get a certificate with `certbot --nginx -d files.example.com`.
These lines matter:

```nginx
client_max_body_size 0;        # otherwise uploads over 1 MB fail with 413
proxy_request_buffering off;   # stream uploads instead of buffering them
proxy_buffering off;
proxy_set_header Host $host;   # S3 signatures include the host name
proxy_set_header X-Forwarded-Proto $scheme;
```

## Tell Tijori its public address

In `tijori.toml`:

```toml
public_url = "https://files.example.com"
```

Then `sudo systemctl restart tijori`. Share links and the admin panel now use this address.

## Make Tijori listen only locally

So nobody can bypass the proxy, set:

```toml
listen = "127.0.0.1:9000"
```

## Check it worked

```sh
curl https://files.example.com/health
```

Then point your apps at `https://files.example.com`.
