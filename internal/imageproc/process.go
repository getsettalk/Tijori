package imageproc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"

	"github.com/gen2brain/webp"
	"golang.org/x/image/draw"
)

var (
	ErrUnsupported   = errors.New("this file is not an image Tijori can resize (supported: JPEG, PNG, GIF, WebP)")
	ErrTooManyPixels = errors.New("image is too large to process")
	ErrBadCrop       = errors.New("crop is outside the image")
)

// Result is an encoded output image.
type Result struct {
	Data        []byte
	ContentType string
	Note        string // explains a choice Tijori made; sent as X-Tijori-Note
}

// animation holds the output frames, each the full canvas.
type animation struct {
	frames []image.Image
	delays []int // milliseconds
	loop   int   // 0 = forever, n = play n times
}

var contentTypes = map[string]string{"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp", "gif": "image/gif"}

// Process decodes src, applies p and encodes the result. By default the
// output has the same format as the original (WebP stays WebP, an animated
// GIF stays animated).
func Process(src []byte, p Params, lim Limits) (Result, error) {
	cfg, srcFmt, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return Result{}, ErrUnsupported
	}
	if lim.MaxPixels > 0 && int64(cfg.Width)*int64(cfg.Height) > lim.MaxPixels {
		return Result{}, fmt.Errorf("%w: %dx%d pixels is over the limit of %d", ErrTooManyPixels, cfg.Width, cfg.Height, lim.MaxPixels)
	}
	out := outputFormat(p, srcFmt)
	var res Result
	tr := &transformer{p: p}

	// Animations stay animated when the output format can hold them.
	var anim *animation
	if !p.Still && (out == "gif" || out == "webp") && (srcFmt == "gif" || srcFmt == "webp") {
		n := frameCount(src, srcFmt)
		budget := min(lim.MaxPixels, 25_000_000)
		if budget <= 0 {
			budget = 25_000_000
		}
		switch {
		case n <= 1:
		case int64(n)*int64(cfg.Width)*int64(cfg.Height) > budget:
			res.Note = fmt.Sprintf("animation with %d frames is too large to resize; first frame only", n)
		case srcFmt == "gif":
			anim, err = gifAnimation(src, tr)
		default:
			anim, err = webpAnimation(src, tr)
		}
		if err != nil {
			return Result{}, err
		}
	}
	if anim == nil {
		img, err := decodeStill(src, srcFmt, cfg)
		if err != nil {
			return Result{}, err
		}
		if img, err = tr.apply(img); err != nil {
			return Result{}, err
		}
		anim = &animation{frames: []image.Image{img}}
	}

	if out == "" { // fmt=auto without WebP support, from a WebP original
		out = "jpeg"
		if hasAlpha(anim.frames[0]) {
			out = "png"
		}
	}
	lossless := p.Lossless || (p.Format == "" && srcFmt == "webp" && webpLossless(src))
	var buf bytes.Buffer
	if err := encode(&buf, out, anim, p, lossless, sourcePalette(src, srcFmt)); err != nil {
		return Result{}, err
	}
	res.Data, res.ContentType = buf.Bytes(), contentTypes[out]
	return res, nil
}

// outputFormat picks the format to write. "" means: JPEG, or PNG when the
// image has transparency (decided after decoding).
func outputFormat(p Params, src string) string {
	switch p.Format {
	case "":
		return src
	case "auto":
		if p.AcceptWebP {
			return "webp"
		}
		if src == "webp" {
			return ""
		}
		return src
	}
	return p.Format
}

// decodeStill decodes one upright frame covering the whole canvas.
func decodeStill(src []byte, format string, cfg image.Config) (image.Image, error) {
	var img image.Image
	var err error
	switch format {
	case "webp":
		img, err = webp.Decode(bytes.NewReader(src), webp.Options{AutoRotate: true})
		if err != nil { // some animated files only decode as a whole
			var all *webp.WEBP
			if all, err = webp.DecodeAll(bytes.NewReader(src), webp.Options{AutoRotate: true}); err == nil && len(all.Image) > 0 {
				img = all.Image[0]
			}
		}
	default:
		img, _, err = image.Decode(bytes.NewReader(src))
	}
	if err != nil || img == nil {
		return nil, ErrUnsupported
	}
	switch format {
	case "jpeg":
		img = applyOrientation(img, jpegOrientation(src))
	case "gif": // the first frame may cover only part of the canvas
		if b := img.Bounds(); b != image.Rect(0, 0, cfg.Width, cfg.Height) && cfg.Width > 0 && cfg.Height > 0 {
			canvas := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
			draw.Draw(canvas, b, img, b.Min, draw.Over)
			img = canvas
		}
	}
	return img, nil
}

// transformer applies rotation, crop and resizing. For animations the
// layout is worked out on the first frame and reused, so frames match.
type transformer struct {
	p      Params
	layout *layout
}

func (t *transformer) apply(img image.Image) (image.Image, error) {
	p := t.p
	if p.Rotate != 0 {
		img = applyOrientation(img, map[int]int{90: 6, 180: 3, 270: 8}[p.Rotate])
	}
	if p.Flip == "h" || p.Flip == "hv" {
		img = applyOrientation(img, 2)
	}
	if p.Flip == "v" || p.Flip == "hv" {
		img = applyOrientation(img, 4)
	}
	b := img.Bounds()
	if !p.Crop.Empty() {
		r := p.Crop.Add(b.Min).Intersect(b)
		if r.Empty() {
			return nil, fmt.Errorf("%w: the image is %dx%d", ErrBadCrop, b.Dx(), b.Dy())
		}
		b = r
	}
	if t.layout == nil {
		smart := func(cw, ch int) image.Point {
			sub := image.Image(img)
			if s, ok := img.(interface {
				SubImage(image.Rectangle) image.Image
			}); ok {
				sub = s.SubImage(b)
			}
			return smartOrigin(sub, cw, ch)
		}
		l := plan(b.Dx(), b.Dy(), p, smart)
		t.layout = &l
	}
	l := *t.layout
	src := l.src.Add(b.Min)
	if l.canvas == src.Size() && l.dst.Min == (image.Point{}) && src == img.Bounds() {
		return img, nil
	}
	dst := image.NewRGBA(image.Rectangle{Max: l.canvas})
	op := draw.Src
	if p.Fit == "pad" && l.dst != dst.Bounds() {
		if p.HasBackground {
			draw.Draw(dst, dst.Bounds(), image.NewUniform(p.Background), image.Point{}, draw.Src)
			op = draw.Over
		}
	}
	if l.dst.Size() == src.Size() {
		draw.Draw(dst, l.dst, img, src.Min, op)
	} else {
		draw.CatmullRom.Scale(dst, l.dst, img, src, op, nil)
	}
	return dst, nil
}

// encode writes anim in the given format.
func encode(buf *bytes.Buffer, out string, anim *animation, p Params, lossless bool, pal color.Palette) error {
	first := anim.frames[0]
	switch out {
	case "png":
		return (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(buf, first)
	case "webp":
		opt := webp.Options{Quality: p.Quality, Lossless: lossless, Method: 4}
		if len(anim.frames) > 1 {
			return webp.EncodeAll(buf, &webp.WEBP{Image: anim.frames, Delay: anim.delays, LoopCount: anim.loop}, opt)
		}
		return webp.Encode(buf, first, opt)
	case "gif":
		g := &gif.GIF{LoopCount: gifLoop(anim.loop)}
		for i, f := range anim.frames {
			g.Image = append(g.Image, toPaletted(f, pal))
			if len(anim.frames) > 1 {
				g.Delay = append(g.Delay, max((anim.delays[i]+5)/10, 2))
				// Every frame is a whole picture; clear it before the next.
				g.Disposal = append(g.Disposal, gif.DisposalBackground)
			}
		}
		if len(anim.frames) == 1 {
			return gif.Encode(buf, g.Image[0], &gif.Options{NumColors: len(g.Image[0].Palette)})
		}
		return gif.EncodeAll(buf, g)
	default:
		img := first
		if hasAlpha(img) {
			bg := color.NRGBA{255, 255, 255, 255}
			if p.HasBackground {
				bg = p.Background
				bg.A = 255
			}
			img = flatten(img, bg)
		}
		return jpeg.Encode(buf, img, &jpeg.Options{Quality: p.Quality})
	}
}

// toPaletted converts a frame for GIF output, keeping the original GIF's
// colours when there is one and transparency when the frame has it.
func toPaletted(img image.Image, pal color.Palette) *image.Paletted {
	if pal == nil {
		pal = append(color.Palette{}, palette.Plan9...)
		if hasAlpha(img) {
			pal[len(pal)-1] = color.RGBA{}
		}
	}
	b := img.Bounds()
	dst := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), pal)
	draw.FloydSteinberg.Draw(dst, dst.Bounds(), img, b.Min)
	return dst
}

func gifLoop(n int) int {
	switch {
	case n == 0:
		return 0
	case n == 1:
		return -1
	}
	return n - 1
}

func hasAlpha(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	return true
}

func flatten(img image.Image, bg color.Color) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}
