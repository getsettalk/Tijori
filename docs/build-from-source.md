# Build the binary yourself

**You will:** build Tijori from the source code, for your own computer or for any other system,
and check that the result is a single file that runs anywhere.

You don't need to do this to use Tijori: the [releases](https://github.com/getsettalk/Tijori/releases)
already have ready binaries, checked on Linux, macOS and Windows after every release. Build it
yourself when you want to read the code first, change something, or target a system without a
ready binary.

## What you need

- **Go 1.26 or newer** ([go.dev/dl](https://go.dev/dl/)). Check with `go version`.
  You don't need to match Tijori's exact Go version: `go.mod` has a `toolchain go1.27.3` line, so
  Go downloads that newer, fully patched version on its own the first time you build.
- **git**.
- Nothing else. Tijori needs no C compiler and no other libraries.

## Build for your computer

```sh
git clone https://github.com/getsettalk/Tijori.git
cd Tijori
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori ./cmd/tijori
./tijori version
```

On Windows (PowerShell):

```powershell
git clone https://github.com/getsettalk/Tijori.git
cd Tijori
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o tijori.exe ./cmd/tijori
.\tijori.exe version
```

What the flags do:

| Flag | Why |
| --- | --- |
| `CGO_ENABLED=0` | No C code: the binary is fully static and runs on any Linux (Ubuntu, Debian, Alpine, BusyBox…) without extra libraries. |
| `-trimpath` | Removes your folder names from the binary. |
| `-ldflags "-s -w"` | Strips debug symbols: smaller file (about 9 MB). |

## Build for another system (cross-compile)

Go builds for any system from any system. Set `GOOS` and `GOARCH`:

| Target | Command |
| --- | --- |
| Linux, Intel/AMD (most VPS) | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori-linux-amd64 ./cmd/tijori` |
| Linux, ARM (Raspberry Pi 4/5, Ampere, Graviton) | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori-linux-arm64 ./cmd/tijori` |
| macOS, Apple Silicon | `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori-macos-arm64 ./cmd/tijori` |
| macOS, Intel | `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori-macos-amd64 ./cmd/tijori` |
| Windows | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori.exe ./cmd/tijori` |
| Linux, 32-bit ARM (older Raspberry Pi) | `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tijori-linux-armv7 ./cmd/tijori` |

Copy the file to the target machine, `chmod +x` it (Linux/macOS) and run it. The first five
targets are tested on real machines for every release; others should work but are not tested.

## Put a version number in your build

```sh
VERSION=1.0.1-custom
go build -trimpath -o tijori \
  -ldflags "-s -w -X github.com/getsettalk/Tijori/internal/version.Version=$VERSION -X github.com/getsettalk/Tijori/internal/version.Commit=$(git rev-parse --short HEAD) -X github.com/getsettalk/Tijori/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  ./cmd/tijori
./tijori version
```

## Check your build

```sh
go vet ./...
go test ./...                                  # add -race on Linux/macOS
go run golang.org/x/vuln/cmd/govulncheck@latest ./...   # known security issues
file tijori                                    # Linux: "statically linked"
./tijori doctor
```

## Build the Docker image

```sh
docker build -t tijori .                       # small image, no ffmpeg
docker build -t tijori:ffmpeg --target ffmpeg .   # with ffmpeg for video
docker run -p 9000:9000 -e TIJORI_ADMIN_PASSWORD=change-me tijori
```

Multi-arch (amd64 + arm64) with Buildx:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t you/tijori:custom --push .
```

## Build a full release locally

The official releases use [GoReleaser](https://goreleaser.com). To make the same archives and
checksums on your machine without publishing anything:

```sh
goreleaser release --snapshot --clean
ls dist/
```

## Verify an official download instead

If you only want to be sure a downloaded release is genuine, compare its checksum with
`checksums.txt` from the same release:

```sh
sha256sum -c --ignore-missing checksums.txt   # Linux
shasum -a 256 -c --ignore-missing checksums.txt   # macOS
```

The [install script](https://github.com/getsettalk/Tijori/blob/main/scripts/install.sh) does
this check for you.

## What you can and can't change

- **Can:** build for any `GOOS`/`GOARCH` Go supports; change the admin panel (plain HTML, CSS and
  JavaScript in `web/admin`, embedded at build time, no Node.js needed); add features.
- **Can't (without work):** WebP *output* and libvips-style image processing need C libraries.
  Tijori stays CGO-free so the binary keeps running everywhere.
