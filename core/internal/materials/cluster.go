package materials

import (
	"fmt"
	"image"
	"math"
	"sort"
)

// Limits of the search for materials. Nothing here is random: the same texture always gives
// the same clusters in the same order.
const (
	// MaxClusters is the most materials a texture is split into.
	MaxClusters = 6
	// sampleSide is the longest side, in texels, of the picture the clusters are found on.
	sampleSide = 256
	// mergeDeltaE merges clusters whose colors differ by less than this: to the eye, one material.
	mergeDeltaE = 10.0
	// minShare folds a cluster smaller than this share of the texels into its neighbour: it is
	// noise, an edge or an anti-aliased line, not a material.
	minShare = 0.01
	// maxPasses bounds the k-means iterations.
	maxPasses = 40
)

// Cluster is one material found in a base color texture.
type Cluster struct {
	center lab
	// RGB is the average color of the texels the cluster owns.
	RGB [3]uint8
	// Share is the part of the texels, from 0 to 1, that are nearest to this cluster.
	Share float64
}

// Hex is the cluster's color as #rrggbb.
func (c Cluster) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.RGB[0], c.RGB[1], c.RGB[2]) }

type sample struct {
	lab lab
	rgb [3]uint8
}

// Find splits a base color texture into at most MaxClusters materials, largest first. It
// works on a copy scaled down to sampleSide texels, so it takes the same few milliseconds for
// any texture size.
func Find(img image.Image) []Cluster {
	pts := scaledSample(pixels(img))
	if len(pts) == 0 {
		return nil
	}
	centers := seed(pts)
	centers, _ = lloyd(pts, centers)
	centers = mergeClose(pts, centers)
	return finish(pts, centers)
}

// scaledSample averages the texture in square blocks so that its longest side is at most
// sampleSide. A block's color is the plain average of its texels in sRGB.
func scaledSample(src *image.RGBA) []sample {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	step := (max(w, h) + sampleSide - 1) / sampleSide
	var out []sample
	for y0 := 0; y0 < h; y0 += step {
		for x0 := 0; x0 < w; x0 += step {
			var r, g, b, n int
			for y := y0; y < min(y0+step, h); y++ {
				row := src.Pix[y*src.Stride:]
				for x := x0; x < min(x0+step, w); x++ {
					r, g, b, n = r+int(row[x*4]), g+int(row[x*4+1]), b+int(row[x*4+2]), n+1
				}
			}
			rgb := [3]uint8{uint8((r + n/2) / n), uint8((g + n/2) / n), uint8((b + n/2) / n)}
			out = append(out, sample{labOf(rgb[0], rgb[1], rgb[2]), rgb})
		}
	}
	return out
}

// seed picks starting centers by farthest point: the color farthest from the average, then
// each time the color farthest from every center so far. Ties go to the first texel.
func seed(pts []sample) []lab {
	var mean lab
	for _, p := range pts {
		for k := range mean {
			mean[k] += p.lab[k]
		}
	}
	for k := range mean {
		mean[k] /= float64(len(pts))
	}
	near := make([]float64, len(pts))
	for i, p := range pts {
		near[i] = p.lab.dist2(mean)
	}
	var centers []lab
	for len(centers) < MaxClusters {
		best := 0
		for i := range pts {
			if near[i] > near[best] {
				best = i
			}
		}
		// Nothing left that looks different: fewer colors than clusters.
		if near[best] < 1 {
			break
		}
		c := pts[best].lab
		centers = append(centers, c)
		for i, p := range pts {
			near[i] = math.Min(near[i], p.lab.dist2(c))
		}
	}
	if len(centers) == 0 {
		centers = append(centers, pts[0].lab)
	}
	return centers
}

// nearest is the index of the center closest to c; the first of equals.
func nearest(centers []lab, c lab) int {
	best, bestD := 0, math.Inf(1)
	for i, k := range centers {
		if d := c.dist2(k); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// lloyd moves the centers to the mean of the texels nearest to each until none changes
// owner, and drops a center that owns nothing. It returns each center's texel count.
func lloyd(pts []sample, centers []lab) ([]lab, []int) {
	owner := make([]int, len(pts))
	for i := range owner {
		owner[i] = -1
	}
	counts := make([]int, len(centers))
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		sums := make([]lab, len(centers))
		counts = make([]int, len(centers))
		for i, p := range pts {
			o := nearest(centers, p.lab)
			if o != owner[i] {
				owner[i], changed = o, true
			}
			counts[o]++
			for k := range p.lab {
				sums[o][k] += p.lab[k]
			}
		}
		for c := range centers {
			if counts[c] > 0 {
				for k := range sums[c] {
					centers[c][k] = sums[c][k] / float64(counts[c])
				}
			}
		}
		if !changed {
			break
		}
	}
	var kept []lab
	var keptCounts []int
	for c := range centers {
		if counts[c] > 0 {
			kept, keptCounts = append(kept, centers[c]), append(keptCounts, counts[c])
		}
	}
	return kept, keptCounts
}

// mergeClose joins the two closest centers while they differ by less than mergeDeltaE, into
// their average weighted by the texels each owns.
func mergeClose(pts []sample, centers []lab) []lab {
	counts := tally(pts, centers)
	for len(centers) > 1 {
		bi, bj, bd := 0, 0, math.Inf(1)
		for i := range centers {
			for j := i + 1; j < len(centers); j++ {
				if d := deltaE(centers[i], centers[j]); d < bd {
					bi, bj, bd = i, j, d
				}
			}
		}
		if bd >= mergeDeltaE {
			break
		}
		wi, wj := float64(max(counts[bi], 1)), float64(max(counts[bj], 1))
		for k := range centers[bi] {
			centers[bi][k] = (centers[bi][k]*wi + centers[bj][k]*wj) / (wi + wj)
		}
		counts[bi] += counts[bj]
		centers = append(centers[:bj], centers[bj+1:]...)
		counts = append(counts[:bj], counts[bj+1:]...)
	}
	return centers
}

// tally counts the texels nearest to each center.
func tally(pts []sample, centers []lab) []int {
	counts := make([]int, len(centers))
	for _, p := range pts {
		counts[nearest(centers, p.lab)]++
	}
	return counts
}

// finish drops the clusters under minShare, smallest first, and describes the rest.
func finish(pts []sample, centers []lab) []Cluster {
	for {
		counts := tally(pts, centers)
		small, smallN := -1, len(pts)
		for i, n := range counts {
			if float64(n) < minShare*float64(len(pts)) && n < smallN {
				small, smallN = i, n
			}
		}
		if small < 0 || len(centers) == 1 {
			break
		}
		// Its texels go to the nearest center left, which tally does on the next turn.
		centers = append(centers[:small], centers[small+1:]...)
	}
	sums := make([][3]int, len(centers))
	counts := make([]int, len(centers))
	for _, p := range pts {
		o := nearest(centers, p.lab)
		counts[o]++
		for k := range sums[o] {
			sums[o][k] += int(p.rgb[k])
		}
	}
	var out []Cluster
	for i, c := range centers {
		if counts[i] == 0 {
			continue
		}
		n := counts[i]
		out = append(out, Cluster{
			center: c, Share: float64(n) / float64(len(pts)),
			RGB: [3]uint8{uint8((sums[i][0] + n/2) / n), uint8((sums[i][1] + n/2) / n), uint8((sums[i][2] + n/2) / n)},
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Share != b.Share {
			return a.Share > b.Share
		}
		for k := range a.center {
			if a.center[k] != b.center[k] {
				return a.center[k] > b.center[k]
			}
		}
		return false
	})
	return out
}
