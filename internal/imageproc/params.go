// Package imageproc serves resized versions of images stored in Tijori via
// GET /img/<bucket>/<key>?w=300&h=200&fit=cover&fmt=jpeg&q=80. Originals are
// never modified; results are cached on disk with LRU eviction.
package imageproc

import (
	"fmt"
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
	Width   int    // 0 = keep aspect from height
	Height  int    // 0 = keep aspect from width
	Fit     string // "contain" or "cover"
	Format  string // "", "jpeg", "png" or "webp" ("" = same as source)
	Quality int
}

// ParseParams validates query parameters against limits.
func ParseParams(q url.Values, lim Limits) (Params, error) {
	p := Params{Fit: "contain", Quality: lim.Quality}
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
	switch f := strings.ToLower(q.Get("fit")); f {
	case "", "contain", "inside":
		p.Fit = "contain"
	case "cover", "crop":
		p.Fit = "cover"
	default:
		return p, fmt.Errorf("fit must be cover or contain")
	}
	switch f := strings.ToLower(q.Get("fmt")); f {
	case "":
	case "jpg", "jpeg":
		p.Format = "jpeg"
	case "png", "webp":
		p.Format = f
	default:
		return p, fmt.Errorf("fmt must be jpeg, png or webp")
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
	return p, nil
}

// String is a canonical form used in cache keys.
func (p Params) String() string {
	return fmt.Sprintf("w=%d&h=%d&fit=%s&fmt=%s&q=%d", p.Width, p.Height, p.Fit, p.Format, p.Quality)
}
