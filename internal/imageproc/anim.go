package imageproc

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"

	"github.com/gen2brain/webp"
	"golang.org/x/image/draw"
)

// frameCount reads how many frames an image has from its structure, without
// decompressing anything, so oversized animations are refused cheaply.
func frameCount(b []byte, format string) int {
	switch format {
	case "gif":
		return gifFrames(b)
	case "webp":
		n := 0
		webpChunks(b, func(id string, _ []byte) bool {
			if id == "ANMF" {
				n++
			}
			return true
		})
		return max(n, 1)
	}
	return 1
}

// gifFrames walks the GIF block structure and counts image descriptors.
func gifFrames(b []byte) int {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
		return 1
	}
	i := 13
	if b[10]&0x80 != 0 {
		i += 3 << ((b[10] & 7) + 1)
	}
	skipBlocks := func() bool {
		for i < len(b) {
			n := int(b[i])
			i++
			if n == 0 {
				return true
			}
			i += n
		}
		return false
	}
	frames := 0
	for i < len(b) {
		switch b[i] {
		case 0x21: // extension: label, then data sub-blocks
			i += 2
			if !skipBlocks() {
				return max(frames, 1)
			}
		case 0x2C: // image descriptor (+ local colour table), LZW size, data
			if i+10 > len(b) {
				return max(frames, 1)
			}
			packed := b[i+9]
			i += 10
			if packed&0x80 != 0 {
				i += 3 << ((packed & 7) + 1)
			}
			i++ // LZW minimum code size
			if !skipBlocks() {
				return max(frames+1, 1)
			}
			frames++
		default: // 0x3B trailer or garbage
			return max(frames, 1)
		}
	}
	return max(frames, 1)
}

// webpChunks calls fn for each top-level RIFF chunk until it returns false.
func webpChunks(b []byte, fn func(id string, data []byte) bool) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return
	}
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		end := i + 8 + n
		if n < 0 || end > len(b) {
			end = len(b)
		}
		if !fn(id, b[i+8:end]) {
			return
		}
		i = end + n%2
	}
}

// webpLossless reports whether a WebP original uses lossless compression,
// so a resized copy can stay lossless (logos, screenshots, line art).
func webpLossless(b []byte) bool {
	lossless := false
	webpChunks(b, func(id string, data []byte) bool {
		switch id {
		case "VP8L":
			lossless = true
			return false
		case "VP8 ":
			return false
		case "ANMF": // the first frame's own chunks follow a 16-byte header
			if len(data) > 20 {
				lossless = string(data[16:20]) == "VP8L"
			}
			return false
		}
		return true
	})
	return lossless
}

// sourcePalette returns a GIF's colours, used again when writing GIF.
func sourcePalette(b []byte, format string) color.Palette {
	if format != "gif" {
		return nil
	}
	cfg, err := gif.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	if pal, ok := cfg.ColorModel.(color.Palette); ok && len(pal) > 0 {
		return pal
	}
	return nil
}

// gifAnimation composes each GIF frame onto the canvas as browsers do
// (honouring disposal), then transforms it right away, so only the small
// output frames stay in memory.
func gifAnimation(src []byte, tr *transformer) (*animation, error) {
	g, err := gif.DecodeAll(bytes.NewReader(src))
	if err != nil || len(g.Image) == 0 {
		return nil, ErrUnsupported
	}
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 {
		b := g.Image[0].Bounds()
		w, h = b.Max.X, b.Max.Y
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	a := &animation{loop: webpLoop(g.LoopCount)}
	for i, fr := range g.Image {
		var saved []byte
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == gif.DisposalPrevious {
			saved = append([]byte(nil), canvas.Pix...)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		out, err := tr.apply(canvas)
		if err != nil {
			return nil, err
		}
		if out == image.Image(canvas) { // unchanged: keep a copy, the canvas moves on
			c := image.NewRGBA(canvas.Bounds())
			copy(c.Pix, canvas.Pix)
			out = c
		}
		a.frames = append(a.frames, out)
		delay := 10
		if i < len(g.Delay) {
			delay = g.Delay[i]
		}
		a.delays = append(a.delays, delay*10)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, fr.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			copy(canvas.Pix, saved)
		}
	}
	return a, nil
}

// webpLoop converts a GIF loop count (0 forever, -1 once, n = n extra
// times) to "play n times" (0 forever).
func webpLoop(n int) int {
	switch {
	case n == 0:
		return 0
	case n < 0:
		return 1
	}
	return n + 1
}

func webpAnimation(src []byte, tr *transformer) (*animation, error) {
	all, err := webp.DecodeAll(bytes.NewReader(src), webp.Options{AutoRotate: true})
	if err != nil || len(all.Image) == 0 {
		return nil, ErrUnsupported
	}
	a := &animation{loop: all.LoopCount}
	for i, fr := range all.Image {
		out, err := tr.apply(fr)
		if err != nil {
			return nil, err
		}
		a.frames = append(a.frames, out)
		delay := 100
		if i < len(all.Delay) {
			delay = all.Delay[i]
		}
		a.delays = append(a.delays, delay)
	}
	return a, nil
}
