# Troubleshooting and FAQ

Start with:

```sh
tijori doctor
journalctl -u tijori -n 50     # service logs on Linux
```

## `SignatureDoesNotMatch`

The secret key is wrong, or the request was changed on the way.

- Copy the secret again (no spaces or line breaks). Create a new key if unsure.
- Turn on **path-style** URLs (`AWS_USE_PATH_STYLE_ENDPOINT=true`, `forcePathStyle: true`).
- Behind a proxy, the `Host` header must reach Tijori unchanged (`proxy_set_header Host $host;` in Nginx).
- The endpoint must not contain the bucket: use `https://files.example.com`, not `https://files.example.com/my-bucket`.

## `RequestTimeTooSkewed`

The clock on your computer or server is more than 15 minutes off. Fix it:

```sh
sudo timedatectl set-ntp true
```

## `InvalidAccessKeyId`

The key does not exist (maybe revoked). Check `tijori key list`.

## `AccessDenied`

The key is not allowed to do this: it is limited to other buckets, it is
read-only or write-only, or you tried to create a bucket with a key that is
not allowed on all buckets. Anonymous requests can only download from public buckets.

## `413 Request Entity Too Large` from Nginx

Nginx blocks uploads over 1 MB by default. Add `client_max_body_size 0;`
(see [HTTPS](https.md)) and reload Nginx.

## `QuotaExceeded` or `EntityTooLarge`

A bucket, server or file-size limit was reached. See [Quotas](quotas.md).

## "Permission denied" on start

The service user cannot write to the data folder:

```sh
sudo chown -R tijori:tijori /var/lib/tijori
```

## "cannot listen on :9000"

Another program uses the port. Pick another one: `listen = ":9100"`.

## I forgot the admin password

```sh
sudo tijori admin password
```

## Files I copied into the data folder by hand do not show sizes or usage

Tijori creates missing metadata when a file is first read. Run
`tijori usage --recalc` to update the counters.

## Can errors come back as JSON instead of XML?

Yes. Send `Accept: application/json` with the request, or set
`error_format = "json"` (see [Configuration](configuration.md#errors-in-json)).

## Can I use virtual-host style URLs (`bucket.files.example.com`)?

Not yet. Use path-style URLs. All major SDKs support them.

## Laravel `Storage::setVisibility()` does nothing

Visibility is per bucket in Tijori (the **Public** switch), not per file.

## Does it work on Windows?

Yes, run `tijori.exe serve`. The `service` command is for Linux and macOS; on
Windows use Task Scheduler or NSSM.
