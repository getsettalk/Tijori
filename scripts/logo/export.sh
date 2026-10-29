#!/bin/sh
# Export the logo SVGs (the source of truth) to PNG for README, docs and app icons.
# Needs rsvg-convert: `brew install librsvg` or `apt install librsvg2-bin`.
#   sh scripts/logo/export.sh
set -eu
cd "$(dirname "$0")/../.."
command -v rsvg-convert >/dev/null || { echo "install rsvg-convert (librsvg) first" >&2; exit 1; }
rsvg-convert -w 512 -h 512 web/admin/logo.svg -o web/admin/logo-512.png
rsvg-convert -w 192 -h 192 web/admin/logo.svg -o web/admin/logo-192.png
rsvg-convert -w 32 -h 32 web/admin/favicon.svg -o web/admin/logo-32.png
cp web/admin/logo.svg docs/images/logo.svg
echo "Exported logo-512.png, logo-192.png, logo-32.png and docs/images/logo.svg"
