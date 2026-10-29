package imageproc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register GIF decoder
	"image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

var (
	ErrUnsupported   = errors.New("this file is not an image Tijori can resize (supported: JPEG, PNG, GIF, WebP)")
	ErrTooManyPixels = errors.New("image is too large to process")
)

// Result is an encoded output image.
type Result struct {
	Data        []byte
	ContentType string
	Ext         string
}

// Process decodes src, resizes it according to p and encodes the result.
// WebP output is not available without CGO, so fmt=webp falls back to JPEG
// (or PNG for images with transparency).
func Process(src []byte, p Params, lim Limits) (Result, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return Result{}, ErrUnsupported
	}
	if lim.MaxPixels > 0 && int64(cfg.Width)*int64(cfg.Height) > lim.MaxPixels {
		return Result{}, fmt.Errorf("%w: %dx%d pixels is over the limit of %d", ErrTooManyPixels, cfg.Width, cfg.Height, lim.MaxPixels)
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return Result{}, ErrUnsupported
	}
	if format == "jpeg" {
		img = applyOrientation(img, jpegOrientation(src))
	}
	img = resize(img, p)

	out := p.Format
	alpha := hasAlpha(img)
	if out == "" {
		out = format
	}
	if out == "webp" || out == "gif" {
		out = "jpeg"
		if alpha {
			out = "png"
		}
	}
	var buf bytes.Buffer
	switch out {
	case "png":
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return Result{}, err
		}
		return Result{buf.Bytes(), "image/png", "png"}, nil
	default:
		if alpha {
			img = flatten(img, color.White)
		}
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: p.Quality}); err != nil {
			return Result{}, err
		}
		return Result{buf.Bytes(), "image/jpeg", "jpg"}, nil
	}
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

// targetSize computes the output size and the source rectangle to sample.
// Images are never enlarged.
func targetSize(sw, sh int, p Params) (ow, oh int, crop image.Rectangle) {
	crop = image.Rect(0, 0, sw, sh)
	if p.Width == 0 && p.Height == 0 {
		return sw, sh, crop
	}
	fw, fh := float64(sw), float64(sh)
	if p.Fit == "cover" && p.Width > 0 && p.Height > 0 {
		aspect := float64(p.Width) / float64(p.Height)
		cw, ch := fw, fh
		if fw/fh > aspect {
			cw = fh * aspect
		} else {
			ch = fw / aspect
		}
		x0, y0 := int(math.Round((fw-cw)/2)), int(math.Round((fh-ch)/2))
		crop = image.Rect(x0, y0, x0+int(math.Round(cw)), y0+int(math.Round(ch)))
		ow, oh = p.Width, p.Height
		if crop.Dx() < ow {
			ow, oh = crop.Dx(), crop.Dy()
		}
		return max(ow, 1), max(oh, 1), crop
	}
	scale := math.Inf(1)
	if p.Width > 0 {
		scale = float64(p.Width) / fw
	}
	if p.Height > 0 {
		scale = math.Min(scale, float64(p.Height)/fh)
	}
	scale = math.Min(scale, 1)
	return max(int(math.Round(fw*scale)), 1), max(int(math.Round(fh*scale)), 1), crop
}

func resize(img image.Image, p Params) image.Image {
	b := img.Bounds()
	ow, oh, crop := targetSize(b.Dx(), b.Dy(), p)
	crop = crop.Add(b.Min)
	if ow == b.Dx() && oh == b.Dy() && crop == b {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, crop, draw.Src, nil)
	return dst
}
