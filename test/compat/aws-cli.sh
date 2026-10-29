#!/bin/sh
# AWS CLI compatibility smoke test.
# Usage: TIJORI_URL=... TIJORI_ACCESS_KEY=... TIJORI_SECRET_KEY=... sh aws-cli.sh
set -eu
export AWS_ACCESS_KEY_ID="$TIJORI_ACCESS_KEY" AWS_SECRET_ACCESS_KEY="$TIJORI_SECRET_KEY" AWS_DEFAULT_REGION=auto
E="--endpoint-url ${TIJORI_URL:-http://localhost:9000}"
B="awscli-compat-$$"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
head -c 12000000 /dev/urandom > "$tmp/big.bin"   # large enough for multipart
aws $E s3 mb "s3://$B"
aws $E s3 cp "$tmp/big.bin" "s3://$B/big.bin"
aws $E s3 cp "s3://$B/big.bin" "$tmp/back.bin"
cmp "$tmp/big.bin" "$tmp/back.bin" && echo "ok   multipart upload/download checksums match"
aws $E s3 ls "s3://$B/"
aws $E s3 presign "s3://$B/big.bin" --expires-in 60 >/dev/null && echo "ok   presign"
aws $E s3 rb "s3://$B" --force
echo "AWS CLI compatibility: all good"
