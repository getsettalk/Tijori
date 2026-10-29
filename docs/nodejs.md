# Connect Node.js

**You will:** upload, download and share files from Node.js with the official AWS SDK v3.

```sh
npm install @aws-sdk/client-s3 @aws-sdk/s3-request-presigner
```

## Create the client

```js
import { S3Client } from "@aws-sdk/client-s3";

export const s3 = new S3Client({
  endpoint: process.env.TIJORI_URL,   // https://files.example.com
  region: "auto",
  forcePathStyle: true,
  credentials: {
    accessKeyId: process.env.TIJORI_ACCESS_KEY,
    secretAccessKey: process.env.TIJORI_SECRET_KEY,
  },
});
```

## Upload, download, list, delete

```js
import { PutObjectCommand, GetObjectCommand, ListObjectsV2Command, DeleteObjectCommand } from "@aws-sdk/client-s3";
import { readFile } from "node:fs/promises";

await s3.send(new PutObjectCommand({
  Bucket: "my-app", Key: "reports/june.pdf",
  Body: await readFile("june.pdf"), ContentType: "application/pdf",
}));

const res = await s3.send(new GetObjectCommand({ Bucket: "my-app", Key: "reports/june.pdf" }));
const bytes = await res.Body.transformToByteArray();

const list = await s3.send(new ListObjectsV2Command({ Bucket: "my-app", Prefix: "reports/" }));
console.log(list.Contents?.map((o) => o.Key));

await s3.send(new DeleteObjectCommand({ Bucket: "my-app", Key: "reports/june.pdf" }));
```

## Big files

Use `@aws-sdk/lib-storage`; it switches to multipart upload automatically:

```js
import { Upload } from "@aws-sdk/lib-storage";
import { createReadStream } from "node:fs";

await new Upload({ client: s3, params: { Bucket: "my-app", Key: "video.mp4", Body: createReadStream("video.mp4") } }).done();
```

## Share links

```js
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

const url = await getSignedUrl(s3, new GetObjectCommand({ Bucket: "my-app", Key: "reports/june.pdf" }), { expiresIn: 3600 });
```

## Check it worked

The repository has a ready test:
`cd test/compat/node && npm install && TIJORI_URL=... TIJORI_ACCESS_KEY=... TIJORI_SECRET_KEY=... node smoke.mjs`
