package materials

import (
	"image"
	"math"
	"runtime"
	"sync"
)

// Value is how rough and how metallic a material is, each from 0 to 1.
type Value struct{ Roughness, Metallic float64 }

// Paint makes the metallic-roughness map: every texel of the base color takes the roughness
// (green) and metallic (blue) of the cluster nearest to its color, and the two channels are
// blurred a little so the borders between materials do not alias. Red, the occlusion that
// shares the map, is copied from existing, or is 255 when there is none. The map is as large
// as existing, else as base.
func Paint(base, existing image.Image, clusters []Cluster, values []Value) *image.RGBA {
	src := pixels(base)
	bw, bh := src.Rect.Dx(), src.Rect.Dy()
	var occ *image.RGBA
	w, h := bw, bh
	if existing != nil {
		occ = pixels(existing)
		w, h = occ.Rect.Dx(), occ.Rect.Dy()
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == 0 || h == 0 || bw == 0 || bh == 0 || len(clusters) == 0 {
		return out
	}
	centers := make([]lab, len(clusters))
	rough := make([]uint8, len(clusters))
	metal := make([]uint8, len(clusters))
	for i, c := range clusters {
		centers[i] = c.center
		rough[i], metal[i] = unit8(values[i].Roughness), unit8(values[i].Metallic)
	}
	g, b := make([]uint8, w*h), make([]uint8, w*h)
	parallel(h, func(y0, y1 int) {
		// Neighbouring texels are mostly the same color: skip their conversion.
		var last [3]uint8
		label := 0
		first := true
		for y := y0; y < y1; y++ {
			row := src.Pix[(y*bh/h)*src.Stride:]
			for x := 0; x < w; x++ {
				p := row[(x*bw/w)*4:]
				if first || p[0] != last[0] || p[1] != last[1] || p[2] != last[2] {
					last = [3]uint8{p[0], p[1], p[2]}
					label = nearest(centers, labOf(p[0], p[1], p[2]))
					first = false
				}
				g[y*w+x], b[y*w+x] = rough[label], metal[label]
			}
		}
	})
	radius := max(1, min(w, h)/1024)
	var wg sync.WaitGroup
	for _, plane := range [][]uint8{g, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			boxBlur(plane, w, h, radius)
		}()
	}
	wg.Wait()
	for y := 0; y < h; y++ {
		o := out.Pix[y*out.Stride:]
		for x := 0; x < w; x++ {
			r := uint8(255)
			if occ != nil {
				r = occ.Pix[y*occ.Stride+x*4]
			}
			o[x*4], o[x*4+1], o[x*4+2], o[x*4+3] = r, g[y*w+x], b[y*w+x], 255
		}
	}
	return out
}

func unit8(v float64) uint8 {
	return uint8(math.Round(math.Min(1, math.Max(0, v)) * 255))
}

// parallel runs fn over bands of rows on every core.
func parallel(rows int, fn func(y0, y1 int)) {
	n := min(runtime.GOMAXPROCS(0), max(1, rows/64))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		y0, y1 := rows*i/n, rows*(i+1)/n
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(y0, y1)
		}()
	}
	wg.Wait()
}

// boxBlur averages each texel with the radius texels around it on both axes; the edges
// repeat their last texel. Integer sums keep the result identical on every machine.
func boxBlur(p []uint8, w, h, radius int) {
	n := 2*radius + 1
	tmp := make([]uint8, len(p))
	parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row, out := p[y*w:(y+1)*w], tmp[y*w:(y+1)*w]
			sum := 0
			for k := -radius; k <= radius; k++ {
				sum += int(row[clamp(k, w)])
			}
			for x := 0; x < w; x++ {
				out[x] = uint8((sum + n/2) / n)
				sum += int(row[clamp(x+radius+1, w)]) - int(row[clamp(x-radius, w)])
			}
		}
	})
	parallel(w, func(x0, x1 int) {
		for x := x0; x < x1; x++ {
			sum := 0
			for k := -radius; k <= radius; k++ {
				sum += int(tmp[clamp(k, h)*w+x])
			}
			for y := 0; y < h; y++ {
				p[y*w+x] = uint8((sum + n/2) / n)
				sum += int(tmp[clamp(y+radius+1, h)*w+x]) - int(tmp[clamp(y-radius, h)*w+x])
			}
		}
	})
}

func clamp(i, n int) int { return min(max(i, 0), n-1) }

// variation is the largest spread of the green and blue channels of a metallic-roughness
// map, in 8-bit steps, and their averages from 0 to 1.
func variation(m image.Image) (spread int, roughness, metallic float64) {
	p := pixels(m)
	w, h := p.Rect.Dx(), p.Rect.Dy()
	if w == 0 || h == 0 {
		return 0, 1, 1
	}
	gmin, gmax, bmin, bmax := 255, 0, 255, 0
	var gs, bs uint64
	for y := 0; y < h; y++ {
		row := p.Pix[y*p.Stride:]
		for x := 0; x < w; x++ {
			g, b := int(row[x*4+1]), int(row[x*4+2])
			gmin, gmax, bmin, bmax = min(gmin, g), max(gmax, g), min(bmin, b), max(bmax, b)
			gs, bs = gs+uint64(g), bs+uint64(b)
		}
	}
	n := float64(w*h) * 255
	return max(gmax-gmin, bmax-bmin), float64(gs) / n, float64(bs) / n
}
