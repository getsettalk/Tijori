package imageproc

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/url"
	"testing"
)

func TestTargetSize(t *testing.T) {
	cases := []struct {
		sw, sh int
		p      Params
		ow, oh int
	}{
		{1000, 500, Params{Width: 300, Fit: "contain"}, 300, 150},
		{1000, 500, Params{Height: 100, Fit: "contain"}, 200, 100},
		{1000, 500, Params{Width: 300, Height: 300, Fit: "contain"}, 300, 150},
		{1000, 500, Params{Width: 300, Height: 300, Fit: "cover"}, 300, 300},
		{100, 50, Params{Width: 300, Fit: "contain"}, 100, 50}, // never enlarge
		{1000, 500, Params{}, 1000, 500},
	}
	for _, c := range cases {
		ow, oh, _ := targetSize(c.sw, c.sh, c.p)
		if ow != c.ow || oh != c.oh {
			t.Errorf("%dx%d %+v -> %dx%d, want %dx%d", c.sw, c.sh, c.p, ow, oh, c.ow, c.oh)
		}
	}
}

func TestParseParams(t *testing.T) {
	lim := Limits{MaxWidth: 2000, MaxHeight: 2000, AllowedWidths: []int{150, 300}, Quality: 80}
	if _, err := ParseParams(url.Values{"w": {"300"}, "fit": {"cover"}, "fmt": {"webp"}}, lim); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []url.Values{{"w": {"301"}}, {"w": {"-1"}}, {"h": {"5000"}}, {"fit": {"stretch"}}, {"fmt": {"bmp"}}, {"q": {"0"}}} {
		if _, err := ParseParams(bad, lim); err == nil {
			t.Errorf("ParseParams(%v) accepted", bad)
		}
	}
}

func testJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

// withOrientation inserts an EXIF APP1 segment with the given orientation.
func withOrientation(j []byte, o byte) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, o, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	n := len(payload) + 2
	seg := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, payload...)
	out := append([]byte{}, j[:2]...)
	out = append(out, seg...)
	return append(out, j[2:]...)
}

func TestProcessResizeAndOrientation(t *testing.T) {
	src := withOrientation(testJPEG(400, 200), 6)
	if o := jpegOrientation(src); o != 6 {
		t.Fatalf("orientation %d", o)
	}
	res, err := Process(src, Params{Width: 100, Fit: "contain", Quality: 80}, Limits{MaxPixels: 1e7})
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(res.Data))
	if err != nil {
		t.Fatal(err)
	}
	// 400x200 rotated 90° is 200x400; width 100 gives 100x200.
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 200 || res.ContentType != "image/jpeg" {
		t.Fatalf("got %v %s", b, res.ContentType)
	}
}

func TestProcessLimitsAndFormats(t *testing.T) {
	if _, err := Process(testJPEG(400, 200), Params{Quality: 80}, Limits{MaxPixels: 1000}); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("expected pixel limit error, got %v", err)
	}
	if _, err := Process([]byte("not an image"), Params{Quality: 80}, Limits{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported, got %v", err)
	}
	// Transparent PNG asked as webp falls back to PNG to keep transparency.
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	res, err := Process(buf.Bytes(), Params{Format: "webp", Quality: 80}, Limits{})
	if err != nil || res.ContentType != "image/png" {
		t.Fatalf("webp fallback: %v %s", err, res.ContentType)
	}
}

func TestCacheEviction(t *testing.T) {
	c, err := NewCache(t.TempDir(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"aa1", "bb2", "cc3", "dd4", "ee5"} {
		c.Put(n, make([]byte, 240))
		c.Get("aa1") // keep the first one hot
	}
	if _, ok := c.Get("aa1"); !ok {
		t.Fatal("recently used entry was evicted")
	}
	if _, ok := c.Get("bb2"); ok {
		t.Fatal("oldest unused entry should be evicted")
	}
	if _, total, _, _ := c.Stats(); total > 1000 {
		t.Fatalf("cache over limit: %d", total)
	}
}
