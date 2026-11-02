package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gen2brain/webp"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/config"
)

func TestImageRoute(t *testing.T) {
	e := startServer(t, func(c *config.Config) {
		c.Buckets["pics"] = config.BucketConfig{PublicRead: true}
		c.Buckets["private"] = config.BucketConfig{}
	})
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1200; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 50, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	for _, b := range []string{"pics", "private"} {
		if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b), Key: aws.String("photo.jpg"), Body: bytes.NewReader(buf.Bytes())}); err != nil {
			t.Fatal(err)
		}
	}

	get := func(url string) (*http.Response, []byte) {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	resp, body := get(e.url + "/img/pics/photo.jpg?w=300")
	if resp.StatusCode != 200 || resp.Header.Get("X-Tijori-Cache") != "MISS" {
		t.Fatalf("first request: %d %s %s", resp.StatusCode, resp.Header.Get("X-Tijori-Cache"), body)
	}
	out, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil || out.Bounds().Dx() != 300 || out.Bounds().Dy() != 200 {
		t.Fatalf("resized: %v %v", err, out.Bounds())
	}
	if len(body) >= buf.Len() {
		t.Fatalf("resized image (%d) not smaller than original (%d)", len(body), buf.Len())
	}
	resp, body2 := get(e.url + "/img/pics/photo.jpg?w=300")
	if resp.Header.Get("X-Tijori-Cache") != "HIT" || !bytes.Equal(body, body2) {
		t.Fatalf("second request not served from cache: %s", resp.Header.Get("X-Tijori-Cache"))
	}

	// Private bucket: anonymous denied, presigned URL allowed.
	if resp, _ := get(e.url + "/img/private/photo.jpg?w=300"); resp.StatusCode != 403 {
		t.Fatalf("private anonymous: %d", resp.StatusCode)
	}
	u, _ := auth.Presign(auth.PresignOptions{Method: "GET", URL: e.url + "/img/private/photo.jpg?w=200&fit=cover&h=200",
		AccessKey: e.key.AccessKey, SecretKey: e.key.SecretKey, Expires: time.Minute})
	if resp, b := get(u); resp.StatusCode != 200 {
		t.Fatalf("presigned img: %d %s", resp.StatusCode, b)
	}

	// Bad parameters and missing images.
	if resp, _ := get(e.url + "/img/pics/photo.jpg?w=99999"); resp.StatusCode != 400 {
		t.Fatalf("huge width: %d", resp.StatusCode)
	}
	if resp, _ := get(e.url + "/img/pics/nope.jpg?w=10"); resp.StatusCode != 404 {
		t.Fatalf("missing: %d", resp.StatusCode)
	}
}

func TestImageRouteKeepsWebP(t *testing.T) {
	e := startServer(t, func(c *config.Config) { c.Buckets["pics"] = config.BucketConfig{PublicRead: true} })
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x / 4), uint8(y / 3), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, webp.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("pics"), Key: aws.String("photo.webp"),
		Body: bytes.NewReader(buf.Bytes()), ContentType: aws.String("image/webp")}); err != nil {
		t.Fatal(err)
	}
	get := func(url, accept string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", url, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	size := func(b []byte) (string, int, int) {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("output does not decode: %v", err)
		}
		return format, cfg.Width, cfg.Height
	}

	// WebP in, WebP out, also when served from the cache.
	for _, state := range []string{"MISS", "HIT"} {
		resp, body := get(e.url+"/img/pics/photo.webp?w=200&h=200&fit=cover&pos=top", "")
		if f, w, h := size(body); resp.StatusCode != 200 || f != "webp" || w != 200 || h != 200 ||
			resp.Header.Get("Content-Type") != "image/webp" || resp.Header.Get("X-Tijori-Cache") != state {
			t.Fatalf("%s: %d %s %dx%d %s", state, resp.StatusCode, f, w, h, resp.Header.Get("Content-Type"))
		}
	}
	// fmt=auto follows the browser and tells caches so.
	resp, body := get(e.url+"/img/pics/photo.webp?w=100&fmt=auto", "image/avif,image/webp,*/*")
	if f, _, _ := size(body); f != "webp" || resp.Header.Get("Vary") != "Accept" {
		t.Fatalf("auto with webp: %s vary=%q", f, resp.Header.Get("Vary"))
	}
	resp, body = get(e.url+"/img/pics/photo.webp?w=100&fmt=auto", "image/png,image/*")
	if f, _, _ := size(body); f != "jpeg" || resp.Header.Get("Vary") != "Accept" {
		t.Fatalf("auto without webp: %s", f)
	}
	// Crop errors are explained.
	if resp, body := get(e.url+"/img/pics/photo.webp?crop=900,900,10,10", ""); resp.StatusCode != 400 || !bytes.Contains(body, []byte("outside")) {
		t.Fatalf("crop outside: %d %s", resp.StatusCode, body)
	}
}
