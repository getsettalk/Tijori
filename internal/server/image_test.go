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
