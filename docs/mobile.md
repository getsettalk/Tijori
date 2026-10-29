# Mobile apps and presigned uploads

**You will:** let a phone app upload straight to Tijori without putting your secret key in the app.

!!! warning "Never ship keys in an app"
    Anyone can extract them. Instead, your server hands out short-lived **presigned URLs**.

## How it works

1. The app asks **your backend**: "I want to upload `photo.jpg`".
2. The backend checks the user and returns a presigned PUT URL (valid for a few minutes).
3. The app uploads the file directly to Tijori with a plain HTTP `PUT`.

## Backend: create the URL

=== "Node.js"

    ```js
    import { PutObjectCommand } from "@aws-sdk/client-s3";
    import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

    app.post("/upload-url", requireLogin, async (req, res) => {
      const key = `users/${req.user.id}/${crypto.randomUUID()}.jpg`;
      const url = await getSignedUrl(s3, new PutObjectCommand({ Bucket: "my-app", Key: key }), { expiresIn: 300 });
      res.json({ url, key });
    });
    ```

=== "Laravel"

    ```php
    Route::post('/upload-url', function (Request $request) {
        $key = 'users/'.$request->user()->id.'/'.Str::uuid().'.jpg';
        ['url' => $url] = Storage::temporaryUploadUrl($key, now()->addMinutes(5));
        return ['url' => $url, 'key' => $key];
    })->middleware('auth:sanctum');
    ```

## App: upload the file

=== "Flutter"

    ```dart
    import 'dart:convert';
    import 'dart:io';
    import 'package:http/http.dart' as http;

    Future<String> uploadPhoto(File file) async {
      final r = await http.post(Uri.parse('https://api.example.com/upload-url'),
          headers: {'Authorization': 'Bearer $token'});
      final data = jsonDecode(r.body);
      final put = await http.put(Uri.parse(data['url']), body: await file.readAsBytes());
      if (put.statusCode != 200) throw Exception('upload failed: ${put.body}');
      return data['key'];
    }
    ```

=== "React Native"

    ```js
    async function uploadPhoto(localUri) {
      const r = await fetch("https://api.example.com/upload-url", {
        method: "POST", headers: { Authorization: `Bearer ${token}` },
      });
      const { url, key } = await r.json();
      const blob = await (await fetch(localUri)).blob();
      const put = await fetch(url, { method: "PUT", body: blob });
      if (!put.ok) throw new Error("upload failed: " + (await put.text()));
      return key;
    }
    ```

To get errors as JSON instead of XML (easier to read in an app), send
`Accept: application/json` with the upload, for example
`fetch(url, { method: "PUT", body: blob, headers: { Accept: "application/json" } })`.
A failed upload then returns `{"error": {"code": "...", "message": "..."}}`.

!!! note "Uploading from a web page (not a native app)"
    Browsers only allow a page's JavaScript to talk to another domain when that domain says so
    (CORS). Tell Tijori which sites may upload:

    ```toml
    cors_origins = ["https://app.example.com"]
    ```

    or `TIJORI_CORS_ORIGINS=https://app.example.com` in Docker/Coolify. Native Flutter and React
    Native apps do not need this.

If you sign a `ContentType` into the URL, the app must send exactly the same
`Content-Type` header. If you do not, leave the header out.

## Showing images

- Public bucket: `https://files.example.com/my-app/users/42/abc.jpg`
- Thumbnail: `https://files.example.com/img/my-app/users/42/abc.jpg?w=300`
- Private bucket: ask your backend for a presigned GET URL (both routes accept them).

## Limits

Set `max_object_size` on the bucket so a presigned URL cannot be used to
upload something huge. See [Quotas](quotas.md).
