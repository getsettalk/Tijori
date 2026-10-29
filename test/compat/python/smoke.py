"""boto3 compatibility smoke test (boto3 is also what the AWS CLI uses).

Usage: pip install boto3 && TIJORI_URL=... TIJORI_ACCESS_KEY=... TIJORI_SECRET_KEY=... python smoke.py
"""
import hashlib
import os
import tempfile
import urllib.request

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

s3 = boto3.client(
    "s3",
    endpoint_url=os.environ.get("TIJORI_URL", "http://localhost:9000"),
    aws_access_key_id=os.environ["TIJORI_ACCESS_KEY"],
    aws_secret_access_key=os.environ["TIJORI_SECRET_KEY"],
    region_name="auto",
    config=Config(s3={"addressing_style": "path"}),
)
bucket = f"boto3-compat-{os.getpid()}"
s3.create_bucket(Bucket=bucket)
print("ok   create bucket")

data = os.urandom(20 * 1024 * 1024)  # 20 MB: upload_file switches to multipart
with tempfile.NamedTemporaryFile(delete=False) as f:
    f.write(data)
s3.upload_file(f.name, bucket, "big/file.bin")
print("ok   upload_file (multipart)")
out = f.name + ".out"
s3.download_file(bucket, "big/file.bin", out)
assert hashlib.md5(open(out, "rb").read()).digest() == hashlib.md5(data).digest()
print("ok   download_file, checksums match")

s3.put_object(Bucket=bucket, Key="a b+c.txt", Body=b"tricky name")
keys = [o["Key"] for o in s3.list_objects_v2(Bucket=bucket)["Contents"]]
assert "a b+c.txt" in keys, keys
print("ok   list (botocore adds and decodes encoding-type=url)")

url = s3.generate_presigned_url("get_object", Params={"Bucket": bucket, "Key": "a b+c.txt"}, ExpiresIn=60)
assert urllib.request.urlopen(url).read() == b"tricky name"
print("ok   presigned GET")

try:
    s3.get_object(Bucket=bucket, Key="missing")
except ClientError as e:
    assert e.response["Error"]["Code"] == "NoSuchKey"
    print("ok   NoSuchKey")

s3.delete_objects(Bucket=bucket, Delete={"Objects": [{"Key": "big/file.bin"}, {"Key": "a b+c.txt"}]})
s3.delete_bucket(Bucket=bucket)
os.remove(f.name)
os.remove(out)
print("boto3 compatibility: all good")
