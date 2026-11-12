package materials

import (
	"image"
	"image/draw"
	"math"
)

// lab is a color in CIELAB under D65, where distance is close to how different two colors
// look.
type lab [3]float64

// srgbToLinear maps an 8-bit sRGB channel to linear light.
var srgbToLinear = func() (t [256]float64) {
	for i := range t {
		c := float64(i) / 255
		if c <= 0.04045 {
			t[i] = c / 12.92
		} else {
			t[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return t
}()

func labOf(r, g, b uint8) lab {
	lr, lg, lb := srgbToLinear[r], srgbToLinear[g], srgbToLinear[b]
	x := (0.4124564*lr + 0.3575761*lg + 0.1804375*lb) / 0.95047
	y := 0.2126729*lr + 0.7151522*lg + 0.0721750*lb
	z := (0.0193339*lr + 0.1191920*lg + 0.9503041*lb) / 1.08883
	fx, fy, fz := labF(x), labF(y), labF(z)
	return lab{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

func labF(t float64) float64 {
	const delta = 6.0 / 29
	if t > delta*delta*delta {
		return math.Cbrt(t)
	}
	return t/(3*delta*delta) + 4.0/29
}

func (a lab) dist2(b lab) float64 {
	dl, da, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dl*dl + da*da + db*db
}

// deltaE is the distance between two colors (CIE76).
func deltaE(a, b lab) float64 { return math.Sqrt(a.dist2(b)) }

// pixels is a picture as 8-bit RGBA rows, whatever it was decoded as.
func pixels(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}
