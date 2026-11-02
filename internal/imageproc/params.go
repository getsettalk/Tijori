// Package imageproc serves resized versions of images stored in Tijori via
// GET /img/<bucket>/<key>?w=300&h=200&fit=cover. Originals are never
// modified; results are cached on disk with LRU eviction.
package imageproc

import (
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Limits come from the [images] config section.
type Limits struct {
	MaxWidth      int
	MaxHeight     int
	MaxPixels     int64
	MaxInputSize  int64
	AllowedWidths []int
	Quality       int
}

// Params is a validated resize request.
type Params struct {
	Width, Height int    // output box after dpr; 0 = follow the aspect ratio
	Fit           string // contain, cover, fill or pad
	Pos           string // gravity for cover and pad (see positions), "smart" or "focus"
	FocusX        float64
	FocusY        float64         // focal point (0-1) when Pos is "focus"
	Crop          image.Rectangle // region of the upright original to use first; empty = all
	Format        string          // "" = same as the original; jpeg, png, webp, gif or auto
	Quality       int             // JPEG and lossy WebP
	Lossless      bool            // lossless WebP
	Background    color.NRGBA     // pad colour and JPEG backdrop
	HasBackground bool            // false = transparent pad (white for JPEG)
	Rotate        int             // 0, 90, 180 or 270 degrees clockwise
	Flip          string          // "", "h", "v" or "hv"
	Still         bool            // anim=0: only the first frame of an animation
	AcceptWebP    bool            // fmt=auto: the client accepts WebP
}

// positions maps gravity names to anchor points (0 = left/top, 1 = right/bottom).
var positions = map[string][2]float64{
	"center": {0.5, 0.5}, "top": {0.5, 0}, "bottom": {0.5, 1}, "left": {0, 0.5}, "right": {1, 0.5},
	"top-left": {0, 0}, "top-right": {1, 0}, "bottom-left": {0, 1}, "bottom-right": {1, 1},
}

// ParseParams validates query parameters against limits. Unknown
// parameters are ignored, so links can carry a cache-busting ?v=2.
func ParseParams(q url.Values, lim Limits) (Params, error) {
	p := Params{Fit: "contain", Pos: "center", Quality: lim.Quality}
	num := func(name string, max int) (int, error) {
		v := q.Get(name)
		if v == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s must be a positive whole number", name)
		}
		if max > 0 && n > max {
			return 0, fmt.Errorf("%s must be at most %d", name, max)
		}
		return n, nil
	}
	var err error
	if p.Width, err = num("w", lim.MaxWidth); err != nil {
		return p, err
	}
	if p.Height, err = num("h", lim.MaxHeight); err != nil {
		return p, err
	}
	if len(lim.AllowedWidths) > 0 && p.Width > 0 && !slices.Contains(lim.AllowedWidths, p.Width) {
		return p, fmt.Errorf("w=%d is not allowed; use one of %v", p.Width, lim.AllowedWidths)
	}
	if v := q.Get("dpr"); v != "" {
		d, err := strconv.ParseFloat(v, 64)
		// Steps of 0.5 keep the number of cached variants small.
		if err != nil || d < 1 || d > 4 || math.Mod(d*2, 1) != 0 {
			return p, fmt.Errorf("dpr must be 1, 1.5, 2, 2.5, 3, 3.5 or 4")
		}
		scale := func(n, max int) int {
			n = int(math.Round(float64(n) * d))
			if max > 0 && n > max {
				n = max
			}
			return n
		}
		p.Width, p.Height = scale(p.Width, lim.MaxWidth), scale(p.Height, lim.MaxHeight)
	}

	switch f := strings.ToLower(q.Get("fit")); f {
	case "", "contain", "inside":
		p.Fit = "contain"
	case "cover", "crop":
		p.Fit = "cover"
	case "fill", "stretch":
		p.Fit = "fill"
	case "pad":
		p.Fit = "pad"
	default:
		return p, fmt.Errorf("fit must be contain, cover, fill or pad")
	}
	pos := strings.ToLower(q.Get("pos"))
	if pos == "" {
		pos = strings.ToLower(q.Get("gravity"))
	}
	switch {
	case pos == "" || pos == "centre":
	case pos == "smart" || pos == "auto":
		p.Pos = "smart"
	default:
		if _, ok := positions[pos]; !ok {
			return p, fmt.Errorf("pos must be center, top, bottom, left, right, top-left, top-right, bottom-left, bottom-right or smart")
		}
		p.Pos = pos
	}
	if v := q.Get("fp"); v != "" {
		xs, ys, ok := strings.Cut(v, ",")
		x, errX := strconv.ParseFloat(xs, 64)
		y, errY := strconv.ParseFloat(ys, 64)
		if !ok || errX != nil || errY != nil || x < 0 || x > 1 || y < 0 || y > 1 {
			return p, fmt.Errorf("fp must be x,y with values from 0 to 1, for example fp=0.5,0.25")
		}
		p.Pos, p.FocusX, p.FocusY = "focus", math.Round(x*1000)/1000, math.Round(y*1000)/1000
	}
	if v := q.Get("crop"); v != "" {
		parts := strings.Split(v, ",")
		var n [4]int
		valid := len(parts) == 4
		for i := 0; valid && i < 4; i++ {
			n[i], err = strconv.Atoi(strings.TrimSpace(parts[i]))
			valid = err == nil && n[i] >= 0 && n[i] <= 1<<20
		}
		if !valid || n[2] == 0 || n[3] == 0 {
			return p, fmt.Errorf("crop must be x,y,width,height in pixels, for example crop=100,50,800,600")
		}
		p.Crop = image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3])
	}

	switch f := strings.ToLower(q.Get("fmt")); f {
	case "", "original", "same":
	case "jpg", "jpeg":
		p.Format = "jpeg"
	case "png", "webp", "gif", "auto":
		p.Format = f
	default:
		return p, fmt.Errorf("fmt must be jpeg, png, webp, gif or auto")
	}
	if v := q.Get("q"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return p, fmt.Errorf("q must be between 1 and 100")
		}
		p.Quality = n
	}
	if p.Quality <= 0 || p.Quality > 100 {
		p.Quality = 80
	}
	if p.Lossless, err = flag(q, "lossless", false); err != nil {
		return p, err
	}
	anim, err := flag(q, "anim", true)
	if err != nil {
		return p, err
	}
	p.Still = !anim
	if v := q.Get("bg"); v != "" {
		c, err := parseColor(v)
		if err != nil {
			return p, err
		}
		p.Background, p.HasBackground = c, c.A > 0
	}
	switch v := q.Get("rot"); v {
	case "", "0", "360":
	case "90", "180", "270":
		p.Rotate, _ = strconv.Atoi(v)
	default:
		return p, fmt.Errorf("rot must be 90, 180 or 270")
	}
	switch v := strings.ToLower(q.Get("flip")); v {
	case "", "h", "v":
		p.Flip = v
	case "hv", "vh", "both":
		p.Flip = "hv"
	default:
		return p, fmt.Errorf("flip must be h, v or hv")
	}
	return p, nil
}

func flag(q url.Values, name string, def bool) (bool, error) {
	switch strings.ToLower(q.Get(name)) {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return def, fmt.Errorf("%s must be 1 or 0", name)
}

// parseColor reads "transparent" or a hex colour (RGB, RGBA, RRGGBB or
// RRGGBBAA, with or without "#").
func parseColor(v string) (color.NRGBA, error) {
	v = strings.TrimPrefix(strings.ToLower(v), "#")
	if v == "transparent" || v == "none" {
		return color.NRGBA{}, nil
	}
	if len(v) == 3 || len(v) == 4 {
		long := make([]byte, 0, 8)
		for i := range len(v) {
			long = append(long, v[i], v[i])
		}
		v = string(long)
	}
	if len(v) == 6 {
		v += "ff"
	}
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != 4 {
		return color.NRGBA{}, fmt.Errorf("bg must be a hex colour like ffffff or 000000cc, or transparent")
	}
	return color.NRGBA{R: b[0], G: b[1], B: b[2], A: b[3]}, nil
}

// String is a canonical form used in cache keys.
func (p Params) String() string {
	s := fmt.Sprintf("w=%d&h=%d&fit=%s&pos=%s&fmt=%s&q=%d", p.Width, p.Height, p.Fit, p.Pos, p.Format, p.Quality)
	if p.Pos == "focus" {
		s += fmt.Sprintf("&fp=%g,%g", p.FocusX, p.FocusY)
	}
	if !p.Crop.Empty() {
		s += fmt.Sprintf("&crop=%d,%d,%d,%d", p.Crop.Min.X, p.Crop.Min.Y, p.Crop.Dx(), p.Crop.Dy())
	}
	if p.Lossless {
		s += "&lossless=1"
	}
	if p.HasBackground {
		s += fmt.Sprintf("&bg=%02x%02x%02x%02x", p.Background.R, p.Background.G, p.Background.B, p.Background.A)
	}
	if p.Rotate != 0 {
		s += fmt.Sprintf("&rot=%d", p.Rotate)
	}
	if p.Flip != "" {
		s += "&flip=" + p.Flip
	}
	if p.Still {
		s += "&anim=0"
	}
	if p.Format == "auto" {
		s += fmt.Sprintf("&webp=%t", p.AcceptWebP)
	}
	return s
}
