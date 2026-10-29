#!/bin/sh
# Tijori installer: downloads the right release for this machine, checks its
# SHA256 checksum and puts the binary on your PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/getsettalk/Tijori/main/scripts/install.sh | sh
#
# Options (environment variables):
#   TIJORI_VERSION=v1.0.0     install a specific version (default: latest)
#   TIJORI_INSTALL_DIR=/path  install somewhere else
set -eu

REPO="getsettalk/Tijori"
say() { printf '%s\n' "$*"; }
fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "this installer needs '$1'; please install it first"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
  fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
  fetch_stdout() { wget -qO- "$1"; }
else
  fail "this installer needs curl or wget"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) fail "unsupported system '$os'. On Windows download tijori.exe from https://github.com/$REPO/releases" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported CPU '$arch' (supported: amd64, arm64)" ;;
esac

version="${TIJORI_VERSION:-}"
if [ -z "$version" ]; then
  version=$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$version" ] || fail "could not find the latest release; set TIJORI_VERSION=v1.0.0"
fi
num="${version#v}"
archive="tijori_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "Downloading Tijori $version for $os/$arch ..."
fetch "$base/$archive" "$tmp/$archive" || fail "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download checksums.txt"

expected=$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else
  fail "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive (expected $expected, got $actual)"
say "Checksum OK."

tar -xzf "$tmp/$archive" -C "$tmp" tijori

dir="${TIJORI_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ "$(id -u)" = "0" ] || [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
  fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/tijori" "$dir/tijori" 2>/dev/null || { cp "$tmp/tijori" "$dir/tijori"; chmod 0755 "$dir/tijori"; }

say ""
say "Tijori $version installed to $dir/tijori"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "Note: $dir is not on your PATH. Add this line to ~/.profile:"
     say "  export PATH=\"$dir:\$PATH\"" ;;
esac
say ""
say "Next steps:"
if [ "$os" = "linux" ]; then
  say "  sudo tijori init               # config in /etc/tijori, data in /var/lib/tijori"
  say "  sudo tijori service install    # start now and on every boot"
else
  say "  tijori init                    # creates ./tijori.toml and ./data"
  say "  tijori serve"
fi
say "  curl http://localhost:9000/health"
