# Contributing to Tijori

Thank you for helping! Tijori aims to stay small and easy to run, so the best
changes are simple ones.

## Before you start

- For bugs, open an issue with steps to reproduce, your OS, and the output of `tijori doctor`.
- For new features, open an issue first so we can agree it fits. Tijori deliberately does not do
  clustering, versioning, object lock or IAM policies.
- Look for issues labelled `good first issue` if you are new.

## Development

You need Go 1.26+. ffmpeg is optional (video tests are skipped without it).

```sh
go build -o tijori ./cmd/tijori
go vet ./...
go test -race ./...
```

Useful layout:

| Folder | What lives there |
| --- | --- |
| `cmd/tijori` | the CLI |
| `internal/storage` | files on disk, metadata, listing, multipart |
| `internal/s3api` | S3 routes, XML and error codes |
| `internal/auth` | SigV4, presigned URLs, access keys |
| `internal/admin`, `web/admin` | admin panel API and its HTML/CSS/JS |
| `internal/imageproc`, `internal/video`, `internal/backup`, `internal/quota` | features |
| `test/compat` | smoke tests for Node, PHP, Python, curl and the AWS CLI |

## Rules of thumb

- Keep dependencies to the standard library and `golang.org/x/...`. Ask before adding anything else.
- No CGO. `CGO_ENABLED=0 go build` must keep working.
- Return real S3 error codes so SDKs behave.
- The admin panel has no build step and a strict Content-Security-Policy: no inline scripts or styles.
- Write user-facing messages and docs in plain, short sentences, and say what to do next.
- Add a test for every bug fix.

## Pull requests

- One topic per PR, with a short description of what and why.
- Update `CHANGELOG.md` under "Unreleased" and the docs if behaviour changes.
- CI must pass (`go vet`, staticcheck, race tests, all build targets, compat tests).

By contributing you agree your work is released under the Apache-2.0 license.
