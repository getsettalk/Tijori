package imageproc

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/url"
	"strings"
	"testing"

	"github.com/gen2brain/webp"
)

func TestPlan(t *testing.T) {
	r := image.Rect
	cases := []struct {
		name   string
		sw, sh int
		p      Params
		canvas image.Point
		src    image.Rectangle
		dst    image.Rectangle
	}{
		{"contain width", 1000, 500, Params{Width: 300, Fit: "contain"}, image.Pt(300, 150), r(0, 0, 1000, 500), r(0, 0, 300, 150)},
		{"contain height", 1000, 500, Params{Height: 100, Fit: "contain"}, image.Pt(200, 100), r(0, 0, 1000, 500), r(0, 0, 200, 100)},
		{"contain box", 1000, 500, Params{Width: 300, Height: 300, Fit: "contain"}, image.Pt(300, 150), r(0, 0, 1000, 500), r(0, 0, 300, 150)},
		{"cover center", 1000, 500, Params{Width: 300, Height: 300, Fit: "cover", Pos: "center"}, image.Pt(300, 300), r(250, 0, 750, 500), r(0, 0, 300, 300)},
		{"cover left", 1000, 500, Params{Width: 300, Height: 300, Fit: "cover", Pos: "left"}, image.Pt(300, 300), r(0, 0, 500, 500), r(0, 0, 300, 300)},
		{"cover right", 1000, 500, Params{Width: 300, Height: 300, Fit: "cover", Pos: "right"}, image.Pt(300, 300), r(500, 0, 1000, 500), r(0, 0, 300, 300)},
		{"cover top of a portrait", 500, 1000, Params{Width: 200, Height: 200, Fit: "cover", Pos: "top"}, image.Pt(200, 200), r(0, 0, 500, 500), r(0, 0, 200, 200)},
		{"cover bottom-right", 500, 1000, Params{Width: 200, Height: 200, Fit: "cover", Pos: "bottom-right"}, image.Pt(200, 200), r(0, 500, 500, 1000), r(0, 0, 200, 200)},
		{"focus point", 1000, 500, Params{Width: 300, Height: 300, Fit: "cover", Pos: "focus", FocusX: 0.3, FocusY: 0.5}, image.Pt(300, 300), r(50, 0, 550, 500), r(0, 0, 300, 300)},
		{"focus point near the edge", 1000, 500, Params{Width: 300, Height: 300, Fit: "cover", Pos: "focus", FocusX: 0.95, FocusY: 0.5}, image.Pt(300, 300), r(500, 0, 1000, 500), r(0, 0, 300, 300)},
		{"fill", 1000, 500, Params{Width: 300, Height: 300, Fit: "fill"}, image.Pt(300, 300), r(0, 0, 1000, 500), r(0, 0, 300, 300)},
		{"pad center", 1000, 500, Params{Width: 300, Height: 300, Fit: "pad", Pos: "center"}, image.Pt(300, 300), r(0, 0, 1000, 500), r(0, 75, 300, 225)},
		{"pad top", 1000, 500, Params{Width: 300, Height: 300, Fit: "pad", Pos: "top"}, image.Pt(300, 300), r(0, 0, 1000, 500), r(0, 0, 300, 150)},
		{"never enlarge", 100, 50, Params{Width: 300, Fit: "contain"}, image.Pt(100, 50), r(0, 0, 100, 50), r(0, 0, 100, 50)},
		{"small source cover", 100, 50, Params{Width: 300, Height: 300, Fit: "cover", Pos: "center"}, image.Pt(50, 50), r(25, 0, 75, 50), r(0, 0, 50, 50)},
		{"cover with one side", 1000, 500, Params{Width: 300, Fit: "cover"}, image.Pt(300, 150), r(0, 0, 1000, 500), r(0, 0, 300, 150)},
		{"nothing asked", 1000, 500, Params{}, image.Pt(1000, 500), r(0, 0, 1000, 500), r(0, 0, 1000, 500)},
	}
	for _, c := range cases {
		l := plan(c.sw, c.sh, c.p, nil)
		if l.canvas != c.canvas || l.src != c.src || l.dst != c.dst {
			t.Errorf("%s: canvas %v src %v dst %v, want %v %v %v", c.name, l.canvas, l.src, l.dst, c.canvas, c.src, c.dst)
		}
	}
}

func TestParseParams(t *testing.T) {
	lim := Limits{MaxWidth: 2000, MaxHeight: 2000, AllowedWidths: []int{150, 300}, Quality: 80}
	p, err := ParseParams(url.Values{"w": {"300"}, "h": {"200"}, "fit": {"cover"}, "pos": {"top"}, "fmt": {"webp"}, "q": {"70"}}, lim)
	if err != nil || p.Width != 300 || p.Fit != "cover" || p.Pos != "top" || p.Format != "webp" || p.Quality != 70 {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = ParseParams(url.Values{"w": {"300"}, "dpr": {"2"}, "fp": {"0.25,0.1"}, "crop": {"10,20,300,400"},
		"bg": {"#f00"}, "rot": {"90"}, "flip": {"h"}, "anim": {"0"}, "lossless": {"1"}, "v": {"cache-buster"}}, lim)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 600 || p.Pos != "focus" || p.FocusX != 0.25 || p.Crop != image.Rect(10, 20, 310, 420) ||
		p.Background != (color.NRGBA{255, 0, 0, 255}) || p.Rotate != 90 || p.Flip != "h" || !p.Still || !p.Lossless {
		t.Fatalf("%+v", p)
	}
	// dpr never goes over the configured maximum.
	if p, _ := ParseParams(url.Values{"w": {"300"}, "dpr": {"4"}}, Limits{MaxWidth: 1000}); p.Width != 1000 {
		t.Fatalf("dpr cap: %d", p.Width)
	}
	if p, _ := ParseParams(url.Values{"fit": {"stretch"}, "gravity": {"smart"}, "fmt": {"auto"}}, lim); p.Fit != "fill" || p.Pos != "smart" || p.Format != "auto" {
		t.Fatalf("aliases: %+v", p)
	}
	for _, bad := range []url.Values{
		{"w": {"301"}}, {"w": {"-1"}}, {"h": {"5000"}}, {"fit": {"zoom"}}, {"fmt": {"bmp"}}, {"q": {"0"}},
		{"pos": {"middle"}}, {"fp": {"2,1"}}, {"fp": {"0.5"}}, {"crop": {"1,2,3"}}, {"crop": {"0,0,0,10"}}, {"crop": {"a,b,c,d"}},
		{"bg": {"zzz"}}, {"rot": {"45"}}, {"flip": {"x"}}, {"dpr": {"5"}}, {"dpr": {"1.3"}}, {"lossless": {"maybe"}}, {"anim": {"2"}},
	} {
		if _, err := ParseParams(bad, lim); err == nil {
			t.Errorf("ParseParams(%v) accepted", bad)
		}
	}
	a, _ := ParseParams(url.Values{"w": {"150"}, "fit": {"cover"}}, lim)
	b, _ := ParseParams(url.Values{"w": {"150"}, "fit": {"cover"}, "pos": {"top"}}, lim)
	if a.String() == b.String() {
		t.Fatal("different positions share a cache key")
	}
}

// gradient is a smooth, low-detail test image.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 100, 255})
		}
	}
	return img
}

func TestSmartCrop(t *testing.T) {
	// A plain grey picture with a busy, colourful patch: smart crop must keep it.
	for _, at := range []int{60, 420} {
		img := image.NewRGBA(image.Rect(0, 0, 600, 200))
		for y := 0; y < 200; y++ {
			for x := 0; x < 600; x++ {
				img.Set(x, y, color.RGBA{128, 128, 128, 255})
				if x >= at && x < at+100 && y >= 50 && y < 150 && (x/5+y/5)%2 == 0 {
					img.Set(x, y, color.RGBA{230, 40, 60, 255})
				}
			}
		}
		o := smartOrigin(img, 200, 200)
		if o.X > at || o.X+200 < at+100 {
			t.Errorf("patch at x=%d: crop starts at %d and misses it", at, o.X)
		}
	}
}

// fill paints r on img.
func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// person draws a standing figure with its head at (x, y).
func person(img *image.RGBA, x, y int) {
	fill(img, image.Rect(x+8, y, x+38, y+35), color.RGBA{224, 172, 105, 255}) // face
	fill(img, image.Rect(x+8, y-6, x+38, y+4), color.RGBA{43, 27, 18, 255})   // hair
	fill(img, image.Rect(x, y+37, x+46, y+102), color.RGBA{220, 38, 38, 255}) // shirt
	fill(img, image.Rect(x+4, y+102, x+42, y+167), color.RGBA{30, 58, 138, 255})
}

func TestSmartCropScenes(t *testing.T) {
	inside := func(lo, hi, start, size int) bool { return start <= lo && start+size >= hi }

	// A landscape: sky, a horizon of hills, and a striped balloon up right.
	land := image.NewRGBA(image.Rect(0, 0, 400, 250))
	for y := 0; y < 250; y++ {
		fill(land, image.Rect(0, y, 400, y+1), color.RGBA{uint8(90 + y/4), uint8(170 + y/6), 240, 255})
	}
	fill(land, image.Rect(0, 170, 400, 250), color.RGBA{60, 120, 70, 255})
	stripes := []color.RGBA{{239, 68, 68, 255}, {245, 158, 11, 255}, {250, 204, 21, 255}, {34, 197, 94, 255}, {59, 130, 246, 255}}
	for i, c := range stripes {
		fill(land, image.Rect(295+i*11, 28, 306+i*11, 95), c)
	}
	if o := smartOrigin(land, 250, 250); !inside(295, 350, o.X, 250) {
		t.Errorf("square crop of the landscape starts at x=%d and cuts the balloon", o.X)
	}
	if o := smartOrigin(land, 400, 100); !inside(28, 95, o.Y, 100) {
		t.Errorf("banner of the landscape starts at y=%d and misses the balloon", o.Y)
	}

	// A person on the left, in front of a wall and a tan (skin-like) floor.
	room := image.NewRGBA(image.Rect(0, 0, 400, 250))
	fill(room, room.Bounds(), color.RGBA{250, 250, 248, 255})
	fill(room, image.Rect(0, 155, 400, 250), color.RGBA{138, 122, 102, 255})
	person(room, 55, 40)
	if o := smartOrigin(room, 250, 250); !inside(55, 101, o.X, 250) {
		t.Errorf("square crop of the room starts at x=%d and misses the person", o.X)
	}
	if o := smartOrigin(room, 400, 100); !inside(34, 75, o.Y, 100) {
		t.Errorf("banner of the room starts at y=%d and cuts the head", o.Y)
	}

	// A tall photo: the face must survive square and banner crops.
	tall := image.NewRGBA(image.Rect(0, 0, 250, 400))
	fill(tall, tall.Bounds(), color.RGBA{250, 250, 248, 255})
	fill(tall, image.Rect(0, 250, 250, 400), color.RGBA{138, 122, 102, 255})
	person(tall, 100, 50)
	for _, h := range []int{250, 62} {
		if o := smartOrigin(tall, 250, h); !inside(44, 85, o.Y, h) {
			t.Errorf("%dx%d crop of the tall photo starts at y=%d and cuts the face", 250, h, o.Y)
		}
	}
}

func encodeJPEG(img image.Image) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

func encodePNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func encodeWebP(img image.Image, lossless bool) []byte {
	var b bytes.Buffer
	if err := webp.Encode(&b, img, webp.Options{Quality: 80, Lossless: lossless}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func decodeSize(t *testing.T, data []byte) (string, image.Point) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
	return format, image.Pt(cfg.Width, cfg.Height)
}

func TestProcessKeepsTheFormat(t *testing.T) {
	src := gradient(400, 200)
	var g bytes.Buffer
	_ = gif.Encode(&g, src, nil)
	cases := []struct {
		name, in string
		data     []byte
		p        Params
		out      string
		lossless bool
	}{
		{"jpeg stays jpeg", "jpeg", encodeJPEG(src), Params{Width: 100}, "jpeg", false},
		{"png stays png", "png", encodePNG(src), Params{Width: 100}, "png", false},
		{"webp stays webp", "webp", encodeWebP(src, false), Params{Width: 100}, "webp", false},
		{"lossless webp stays lossless", "webp", encodeWebP(src, true), Params{Width: 100}, "webp", true},
		{"gif stays gif", "gif", g.Bytes(), Params{Width: 100}, "gif", false},
		{"jpeg to webp", "jpeg", encodeJPEG(src), Params{Width: 100, Format: "webp"}, "webp", false},
		{"webp to jpeg", "webp", encodeWebP(src, false), Params{Width: 100, Format: "jpeg"}, "jpeg", false},
		{"auto with webp support", "jpeg", encodeJPEG(src), Params{Width: 100, Format: "auto", AcceptWebP: true}, "webp", false},
		{"auto without webp support", "jpeg", encodeJPEG(src), Params{Width: 100, Format: "auto"}, "jpeg", false},
		{"auto from webp, no support", "webp", encodeWebP(src, false), Params{Width: 100, Format: "auto"}, "jpeg", false},
	}
	for _, c := range cases {
		c.p.Fit, c.p.Pos, c.p.Quality = "contain", "center", 80
		res, err := Process(c.data, c.p, Limits{MaxPixels: 1e7})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		format, size := decodeSize(t, res.Data)
		if format != c.out || res.ContentType != "image/"+c.out || size != image.Pt(100, 50) {
			t.Errorf("%s: got %s (%s) %v", c.name, format, res.ContentType, size)
		}
		if c.out == "webp" && webpLossless(res.Data) != c.lossless {
			t.Errorf("%s: lossless=%v", c.name, webpLossless(res.Data))
		}
	}
	// A transparent WebP shown to a browser without WebP support becomes PNG.
	clear := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	res, err := Process(encodeWebP(clear, false), Params{Format: "auto", Fit: "contain", Pos: "center", Quality: 80}, Limits{})
	if err != nil || res.ContentType != "image/png" {
		t.Fatalf("transparent auto: %v %s", err, res.ContentType)
	}
}

// testGIF builds an animation whose frames only cover part of the canvas,
// like real GIFs do.
func testGIF(frames, w, h int) []byte {
	g := &gif.GIF{Config: image.Config{Width: w, Height: h, ColorModel: color.Palette(palette.Plan9)}}
	for i := 0; i < frames; i++ {
		fr := image.NewPaletted(image.Rect(i*5, i*5, w/2+i*5, h/2+i*5), palette.Plan9)
		for p := range fr.Pix {
			fr.Pix[p] = uint8(40 * (i + 1))
		}
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, 7)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func TestAnimations(t *testing.T) {
	lim := Limits{MaxPixels: 1e7}
	src := testGIF(3, 120, 80)
	if n := frameCount(src, "gif"); n != 3 {
		t.Fatalf("frameCount = %d", n)
	}
	p := Params{Width: 60, Fit: "contain", Pos: "center", Quality: 80}
	res, err := Process(src, p, lim)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(res.Data))
	if err != nil || len(g.Image) != 3 || g.Delay[0] != 7 || g.Config.Width != 60 || g.Config.Height != 40 {
		t.Fatalf("animated gif: %v frames=%d", err, len(g.Image))
	}

	// GIF to animated WebP, and back.
	p.Format = "webp"
	res, err = Process(src, p, lim)
	if err != nil {
		t.Fatal(err)
	}
	all, err := webp.DecodeAll(bytes.NewReader(res.Data))
	if err != nil || len(all.Image) != 3 || all.Image[0].Bounds().Dx() != 60 || all.Delay[0] != 70 {
		t.Fatalf("gif to webp: %v %d", err, len(all.Image))
	}
	if n := frameCount(res.Data, "webp"); n != 3 {
		t.Fatalf("webp frameCount = %d", n)
	}
	p.Format, p.Width = "", 30
	res2, err := Process(res.Data, p, lim)
	if err != nil {
		t.Fatal(err)
	}
	if all, err := webp.DecodeAll(bytes.NewReader(res2.Data)); err != nil || len(all.Image) != 3 || all.Image[0].Bounds().Dx() != 30 {
		t.Fatalf("webp stays animated: %v", err)
	}
	p.Format = "gif"
	res3, err := Process(res.Data, p, lim)
	if err != nil {
		t.Fatal(err)
	}
	if g, err := gif.DecodeAll(bytes.NewReader(res3.Data)); err != nil || len(g.Image) != 3 {
		t.Fatalf("webp to gif: %v", err)
	}

	// anim=0 gives the first frame; a too-large animation does too, with a note.
	p.Format, p.Still = "", true
	if res, err := Process(src, p, lim); err != nil || frameCount(res.Data, "gif") != 1 {
		t.Fatalf("still: %v", err)
	}
	p.Still = false
	res, err = Process(testGIF(5, 120, 80), p, Limits{MaxPixels: 40000})
	if err != nil || frameCount(res.Data, "gif") != 1 || !strings.Contains(res.Note, "first frame") {
		t.Fatalf("budget: %v %q", err, res.Note)
	}
}

func TestPadCropRotate(t *testing.T) {
	red := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := range red.Pix {
		red.Pix[i] = [4]uint8{220, 20, 20, 255}[i%4]
	}
	lim := Limits{MaxPixels: 1e7}
	base := Params{Width: 100, Height: 100, Fit: "pad", Pos: "center", Quality: 80}

	res, err := Process(encodePNG(red), base, lim)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(res.Data))
	if img.Bounds() != image.Rect(0, 0, 100, 100) {
		t.Fatalf("pad size %v", img.Bounds())
	}
	if _, _, _, a := img.At(50, 5).RGBA(); a != 0 {
		t.Fatal("pad without bg should be transparent")
	}
	if r, _, _, _ := img.At(50, 50).RGBA(); r>>8 < 200 {
		t.Fatal("picture missing from the middle")
	}

	withBG := base
	withBG.Background, withBG.HasBackground = color.NRGBA{0, 0, 255, 255}, true
	res, _ = Process(encodePNG(red), withBG, lim)
	img, _ = png.Decode(bytes.NewReader(res.Data))
	if _, _, b, _ := img.At(50, 5).RGBA(); b>>8 != 255 {
		t.Fatal("pad bg colour not used")
	}

	// Transparent areas become white (or bg) in JPEG.
	jp := base
	jp.Format = "jpeg"
	res, _ = Process(encodePNG(red), jp, lim)
	j, _ := jpeg.Decode(bytes.NewReader(res.Data))
	if r, g, b, _ := j.At(50, 5).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatal("jpeg pad should be white")
	}

	src := encodePNG(gradient(400, 200))
	crop := Params{Fit: "contain", Pos: "center", Quality: 80, Crop: image.Rect(10, 20, 40, 60)}
	res, err = Process(src, crop, lim)
	if _, size := decodeSize(t, res.Data); err != nil || size != image.Pt(30, 40) {
		t.Fatalf("crop: %v %v", err, size)
	}
	crop.Crop = image.Rect(500, 500, 600, 600)
	if _, err := Process(src, crop, lim); !errors.Is(err, ErrBadCrop) {
		t.Fatalf("crop outside: %v", err)
	}

	rot := Params{Fit: "contain", Pos: "center", Quality: 80, Rotate: 90}
	res, _ = Process(src, rot, lim)
	if _, size := decodeSize(t, res.Data); size != image.Pt(200, 400) {
		t.Fatalf("rot=90 size %v", size)
	}
	flip := Params{Fit: "contain", Pos: "center", Quality: 80, Flip: "h"}
	res, _ = Process(src, flip, lim)
	img, _ = png.Decode(bytes.NewReader(res.Data))
	if r, _, _, _ := img.At(0, 0).RGBA(); r>>8 < 200 {
		t.Fatal("flip=h should put the red (right) side on the left")
	}
}

func testJPEG(w, h int) []byte { return encodeJPEG(gradient(w, h)) }

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
	res, err := Process(src, Params{Width: 100, Fit: "contain", Pos: "center", Quality: 80}, Limits{MaxPixels: 1e7})
	if err != nil {
		t.Fatal(err)
	}
	// 400x200 rotated 90° is 200x400; width 100 gives 100x200.
	if format, size := decodeSize(t, res.Data); format != "jpeg" || size != image.Pt(100, 200) {
		t.Fatalf("got %s %v", format, size)
	}
}

func TestProcessLimits(t *testing.T) {
	if _, err := Process(testJPEG(400, 200), Params{Quality: 80}, Limits{MaxPixels: 1000}); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("expected pixel limit error, got %v", err)
	}
	if _, err := Process([]byte("not an image"), Params{Quality: 80}, Limits{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

// FuzzFrameCount feeds random bytes to the structure parsers, which read
// untrusted uploads before anything is decoded.
func FuzzFrameCount(f *testing.F) {
	f.Add(testGIF(2, 20, 20))
	f.Add(encodeWebP(gradient(8, 8), true))
	f.Add([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00"))
	f.Add([]byte("RIFF\xff\xff\xff\xffWEBPANMF\xff\xff\xff\x7f"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if n := frameCount(b, "gif"); n < 1 {
			t.Fatal("gif frames < 1")
		}
		if n := frameCount(b, "webp"); n < 1 {
			t.Fatal("webp frames < 1")
		}
		webpLossless(b)
	})
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
