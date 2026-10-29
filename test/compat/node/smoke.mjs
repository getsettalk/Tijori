// Node.js compatibility smoke test.
// Usage: TIJORI_URL=http://localhost:9000 TIJORI_ACCESS_KEY=... TIJORI_SECRET_KEY=... node smoke.mjs
import { S3Client, CreateBucketCommand, PutObjectCommand, GetObjectCommand, ListObjectsV2Command,
  DeleteObjectCommand, DeleteBucketCommand } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { createHash, randomBytes } from "node:crypto";

const endpoint = process.env.TIJORI_URL ?? "http://localhost:9000";
const s3 = new S3Client({
  endpoint, region: "auto", forcePathStyle: true,
  credentials: { accessKeyId: process.env.TIJORI_ACCESS_KEY, secretAccessKey: process.env.TIJORI_SECRET_KEY },
});
const md5 = (b) => createHash("md5").update(b).digest("hex");
const bucket = "node-compat-" + Date.now();
const check = (ok, msg) => { if (!ok) { console.error("FAIL:", msg); process.exit(1); } console.log("ok  ", msg); };

await s3.send(new CreateBucketCommand({ Bucket: bucket }));
check(true, "create bucket");

const body = randomBytes(256 * 1024);
await s3.send(new PutObjectCommand({ Bucket: bucket, Key: "hello/world.bin", Body: body, ContentType: "application/octet-stream" }));
check(true, "put object (Buffer)");

const got = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "hello/world.bin" }));
const bytes = Buffer.from(await got.Body.transformToByteArray());
check(md5(bytes) === md5(body), "get object, checksums match");

const list = await s3.send(new ListObjectsV2Command({ Bucket: bucket, Prefix: "hello/" }));
check(list.KeyCount === 1, "list objects");

const putUrl = await getSignedUrl(s3, new PutObjectCommand({ Bucket: bucket, Key: "presigned.txt" }), { expiresIn: 300 });
const putRes = await fetch(putUrl, { method: "PUT", body: "from a phone" });
check(putRes.ok, "presigned PUT via fetch");
const getUrl = await getSignedUrl(s3, new GetObjectCommand({ Bucket: bucket, Key: "presigned.txt" }), { expiresIn: 300 });
const text = await (await fetch(getUrl)).text();
check(text === "from a phone", "presigned GET via fetch");

const bad = new S3Client({ endpoint, region: "auto", forcePathStyle: true,
  credentials: { accessKeyId: process.env.TIJORI_ACCESS_KEY, secretAccessKey: "wrong-secret-wrong-secret" } });
try { await bad.send(new ListObjectsV2Command({ Bucket: bucket })); check(false, "wrong secret rejected"); }
catch (e) { check(e.name === "SignatureDoesNotMatch", "wrong secret -> " + e.name); }

for (const Key of ["hello/world.bin", "presigned.txt"]) await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key }));
await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
check(true, "cleanup");
console.log("Node.js SDK compatibility: all good");
