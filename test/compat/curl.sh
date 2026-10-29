#!/bin/sh
# curl --aws-sigv4 compatibility smoke test (curl 7.75+).
# Usage: TIJORI_URL=http://localhost:9000 TIJORI_ACCESS_KEY=... TIJORI_SECRET_KEY=... sh curl.sh
set -eu
U="${TIJORI_URL:-http://localhost:9000}"
AUTH="${TIJORI_ACCESS_KEY}:${TIJORI_SECRET_KEY}"
SIG="aws:amz:auto:s3"
B="curl-compat-$$"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
ok() { echo "ok   $*"; }
fail() { echo "FAIL $*" >&2; exit 1; }
s3() { curl -sf --aws-sigv4 "$SIG" --user "$AUTH" "$@"; }

s3 -X PUT "$U/$B" >/dev/null && ok "create bucket"
head -c 200000 /dev/urandom > "$tmp/in.bin"
s3 -T "$tmp/in.bin" "$U/$B/dir/file.bin" >/dev/null && ok "upload"
s3 "$U/$B/dir/file.bin" -o "$tmp/out.bin" && ok "download"
cmp "$tmp/in.bin" "$tmp/out.bin" && ok "checksums match"
s3 "$U/$B?list-type=2" | grep -q "<Key>dir/file.bin</Key>" && ok "list"
code=$(curl -s -o /dev/null -w '%{http_code}' --aws-sigv4 "$SIG" --user "${TIJORI_ACCESS_KEY}:wrong-secret-wrong" "$U/$B/dir/file.bin")
[ "$code" = 403 ] || fail "wrong secret returned $code, expected 403"
ok "wrong secret -> 403"
code=$(curl -s -o /dev/null -w '%{http_code}' --path-as-is --aws-sigv4 "$SIG" --user "$AUTH" -T "$tmp/in.bin" "$U/$B/../escape")
# 400 when Tijori sees "..". Behind a proxy that cleans the path first, the
# signature no longer matches, so 403. Anything else is a failure.
case "$code" in
  400|403) ok "path traversal rejected ($code)" ;;
  *) fail "path traversal returned $code" ;;
esac
s3 -X DELETE "$U/$B/dir/file.bin" && s3 -X DELETE "$U/$B" && ok "cleanup"
echo "curl --aws-sigv4 compatibility: all good"
