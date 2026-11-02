package imageproc

import (
	"image"
	"math"
	"slices"

	"golang.org/x/image/draw"
)

// layout says which part of the source to use and where it goes.
type layout struct {
	src    image.Rectangle // region of the source image
	canvas image.Point     // output size
	dst    image.Rectangle // where src lands on the canvas (smaller than it for pad)
}

// plan works out the layout for a source of size sw x sh. Images are never
// enlarged, except by fit=fill, which asks for an exact size.
func plan(sw, sh int, p Params, smart func(cw, ch int) image.Point) layout {
	full := image.Rect(0, 0, sw, sh)
	l := layout{src: full, canvas: image.Pt(sw, sh), dst: full}
	w, h := p.Width, p.Height
	if w == 0 && h == 0 {
		return l
	}
	fw, fh := float64(sw), float64(sh)
	both := w > 0 && h > 0
	switch {
	case p.Fit == "fill" && both:
		l.canvas = image.Pt(w, h)
		l.dst = image.Rect(0, 0, w, h)
		return l
	case p.Fit == "cover" && both:
		aspect := float64(w) / float64(h)
		cw, ch := fw, fh
		if fw/fh > aspect {
			cw = fh * aspect
		} else {
			ch = fw / aspect
		}
		icw, ich := max(int(math.Round(cw)), 1), max(int(math.Round(ch)), 1)
		at := cropOrigin(sw, sh, icw, ich, p, smart)
		l.src = image.Rectangle{at, at.Add(image.Pt(icw, ich))}
		ow, oh := w, h
		if icw < ow { // the source is smaller than the box: keep its size
			ow, oh = icw, ich
		}
		l.canvas = image.Pt(max(ow, 1), max(oh, 1))
		l.dst = image.Rectangle{Max: l.canvas}
		return l
	}
	scale := math.Inf(1)
	if w > 0 {
		scale = float64(w) / fw
	}
	if h > 0 {
		scale = math.Min(scale, float64(h)/fh)
	}
	scale = math.Min(scale, 1)
	cw, ch := max(int(math.Round(fw*scale)), 1), max(int(math.Round(fh*scale)), 1)
	l.canvas = image.Pt(cw, ch)
	l.dst = image.Rect(0, 0, cw, ch)
	if p.Fit == "pad" && both {
		l.canvas = image.Pt(w, h)
		ax, ay := anchor(p)
		off := image.Pt(int(math.Round(float64(w-cw)*ax)), int(math.Round(float64(h-ch)*ay)))
		l.dst = image.Rectangle{off, off.Add(image.Pt(cw, ch))}
	}
	return l
}

// anchor returns the gravity point for pos (smart and focus centre the
// image inside a pad).
func anchor(p Params) (float64, float64) {
	if a, ok := positions[p.Pos]; ok {
		return a[0], a[1]
	}
	if p.Pos == "focus" {
		return p.FocusX, p.FocusY
	}
	return 0.5, 0.5
}

// cropOrigin places a cw x ch window inside sw x sh.
func cropOrigin(sw, sh, cw, ch int, p Params, smart func(cw, ch int) image.Point) image.Point {
	if p.Pos == "smart" && smart != nil {
		return smart(cw, ch)
	}
	if p.Pos == "focus" {
		// Centre the window on the focal point, keeping it inside the image.
		x := int(math.Round(p.FocusX*float64(sw) - float64(cw)/2))
		y := int(math.Round(p.FocusY*float64(sh) - float64(ch)/2))
		return image.Pt(min(max(x, 0), sw-cw), min(max(y, 0), sh-ch))
	}
	ax, ay := anchor(p)
	return image.Pt(int(math.Round(float64(sw-cw)*ax)), int(math.Round(float64(sh-ch)*ay)))
}

// smartOrigin finds where a cw x ch crop window keeps the most eye-catching
// part of img. Each pixel scores by how much its colour stands out from its
// surroundings and, less, from the whole picture (measured in Lab, where
// distances match what people see), plus colour detail and skin tones.
// Large even areas (sky, grass) and lines across the whole picture (a
// horizon) score low; objects score high. Content near the window's edges
// counts less, so objects are kept whole rather than cut.
// It works on a small copy, so it is fast.
func smartOrigin(img image.Image, cw, ch int) image.Point {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if cw >= sw && ch >= sh {
		return image.Point{}
	}
	const side = 160
	scale := math.Min(1, side/math.Max(float64(sw), float64(sh)))
	tw, th := max(int(float64(sw)*scale), 2), max(int(float64(sh)*scale), 2)
	small := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, b, draw.Src, nil)

	lab := make([][3]float64, tw*th)
	alpha := make([]float64, tw*th)
	skin := make([]bool, tw*th)
	var mean [3]float64
	weight := 0.0
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			i, j := small.PixOffset(x, y), y*tw+x
			r, g, bl := float64(small.Pix[i]), float64(small.Pix[i+1]), float64(small.Pix[i+2])
			a := float64(small.Pix[i+3]) / 255
			if a > 0 { // un-premultiply
				r, g, bl = r/a, g/a, bl/a
			}
			lab[j], alpha[j] = toLab(r, g, bl), a
			skin[j] = isSkin(r, g, bl)
			for k := range 3 {
				mean[k] += lab[j][k] * a
			}
			weight += a
		}
	}
	if weight == 0 {
		return image.Pt((sw-cw)/2, (sh-ch)/2)
	}
	for k := range 3 {
		mean[k] /= weight
	}
	// A summed-area table gives the average colour around each pixel.
	W := tw + 1
	sat := make([][3]float64, W*(th+1))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			for k := range 3 {
				sat[(y+1)*W+x+1][k] = lab[y*tw+x][k] + sat[y*W+x+1][k] + sat[(y+1)*W+x][k] - sat[y*W+x][k]
			}
		}
	}
	rad := max(2, max(tw, th)/8)
	dist := func(a, b [3]float64) float64 {
		return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
	}
	score := make([]float64, tw*th)
	for y := 0; y < th; y++ {
		y0, y1 := max(y-rad, 0), min(y+rad+1, th)
		for x := 0; x < tw; x++ {
			x0, x1 := max(x-rad, 0), min(x+rad+1, tw)
			n := float64((x1 - x0) * (y1 - y0))
			var local [3]float64
			for k := range 3 {
				local[k] = (sat[y1*W+x1][k] - sat[y0*W+x1][k] - sat[y1*W+x0][k] + sat[y0*W+x0][k]) / n
			}
			j := y*tw + x
			c := lab[j]
			s := 2*dist(c, local) + 0.5*dist(c, mean)
			if x > 0 && y > 0 {
				s += 2 * (dist(c, lab[j-1]) + dist(c, lab[j-tw])) // colour detail
			}
			if skin[j] {
				s += 500 // faces matter most in photos
			}
			score[j] = s * alpha[j]
		}
	}
	// The window spans the whole image in one direction, so only one axis
	// moves: sum the scores per column (or row) and slide over them.
	horizontal := cw < sw
	n, win := th, int(math.Round(float64(ch)*scale))
	if horizontal {
		n, win = tw, int(math.Round(float64(cw)*scale))
	}
	win = min(max(win, 1), n)
	// A pixel only counts with what stands out from the typical value of its
	// row and of its column, so bands across the picture (a horizon, sky,
	// floor, a wall) drop out and objects remain.
	rowMed, colMed := make([]float64, th), make([]float64, tw)
	vals := make([]float64, max(tw, th))
	for y := range th {
		copy(vals, score[y*tw:(y+1)*tw])
		rowMed[y] = median(vals[:tw])
	}
	for x := range tw {
		for y := range th {
			vals[y] = score[y*tw+x]
		}
		colMed[x] = median(vals[:th])
	}
	lines := make([]float64, n)
	for y := range th {
		for x := range tw {
			v := math.Max(0, score[y*tw+x]-math.Max(rowMed[y], colMed[x]))
			if horizontal {
				lines[x] += v
			} else {
				lines[y] += v
			}
		}
	}
	// Inside the window, content fades out over the outer 15% on each side.
	weights := make([]float64, win)
	for i := range weights {
		t := (float64(i) + 0.5) / float64(win)
		weights[i] = math.Min(1, math.Min(t, 1-t)/0.15)
	}
	best, bestAt := -1.0, 0
	for start := 0; start+win <= n; start++ {
		sum := 0.0
		for i, w := range weights {
			sum += lines[start+i] * w
		}
		// Up to 5% less score away from the middle, so near-ties go to the
		// centre; when moving up and down, up to 8% less towards the bottom,
		// because heads are usually above feet and floors.
		off := math.Abs(float64(start)+float64(win)/2-float64(n)/2) / (float64(n) / 2)
		bias := 1 - 0.05*off
		if !horizontal && n > win {
			bias -= 0.08 * float64(start) / float64(n-win)
		}
		if v := sum * bias; v > best {
			best, bestAt = v, start
		}
	}
	if horizontal {
		return image.Pt(min(int(math.Round(float64(bestAt)/scale)), sw-cw), 0)
	}
	return image.Pt(0, min(int(math.Round(float64(bestAt)/scale)), sh-ch))
}

// isSkin is a common skin-tone test in YCbCr, plus a green/red ratio that
// keeps out red clothing.
func isSkin(r, g, b float64) bool {
	cb := 128 - 0.168736*r - 0.331264*g + 0.5*b
	cr := 128 + 0.5*r - 0.418688*g - 0.081312*b
	return r > 95 && cb >= 77 && cb <= 127 && cr >= 133 && cr <= 173 && g >= 0.4*r && g <= 0.9*r
}

// median returns the middle value of v (v is reordered).
func median(v []float64) float64 {
	slices.Sort(v)
	return v[len(v)/2]
}

// toLab converts an sRGB colour (0-255) to CIE Lab (D65), where distances
// match how different colours look to people.
func toLab(r, g, b float64) [3]float64 {
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	R, G, B := lin(r), lin(g), lin(b)
	x := (0.4124*R + 0.3576*G + 0.1805*B) / 0.95047
	y := 0.2126*R + 0.7152*G + 0.0722*B
	z := (0.0193*R + 0.1192*G + 0.9505*B) / 1.08883
	f := func(t float64) float64 {
		if t > 0.008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116
	}
	fx, fy, fz := f(x), f(y), f(z)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}
