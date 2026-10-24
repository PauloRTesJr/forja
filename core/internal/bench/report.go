package bench

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/color"
	_ "image/jpeg" // reference images come as PNG or JPEG; WebP gets no thumbnail
	"image/png"
	"os"
	"time"
)

//go:embed report.html
var reportPage string

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"secs": func(s float64) string { return fmt.Sprintf("%.2f s", s) },
	"ms":   func(s float64) string { return fmt.Sprintf("%.0f ms", s*1000) },
	"bytes": func(n *int64) string {
		if n == nil {
			return "—"
		}
		return fmt.Sprintf("%.2f GB", float64(*n)/(1<<30))
	},
	"total": func(n int64) string {
		if n == 0 {
			return "—"
		}
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	},
	"load": func(s Spread) string {
		if s.N == 0 {
			return "—"
		}
		return fmt.Sprintf("%.2f s", s.Median)
	},
	"util": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.0f%%", *p)
	},
	"share": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.0f%%", *p*100)
	},
	"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"count": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.0f", *p)
	},
	"budget": func(b float64) string {
		if b == 0 {
			return "—"
		}
		return fmt.Sprintf("%.0f s", b)
	},
	"over": func(spread Spread, budget float64) bool { return budget > 0 && spread.N > 0 && spread.Median > budget },
	"overMS": func(spread Spread, budget float64) bool {
		return budget > 0 && spread.N > 0 && spread.Median*1000 > budget
	},
	"date":  func(t time.Time) string { return t.Format("2006-01-02 15:04") },
	"short": func(s string) string { return s[:min(len(s), 12)] },
	"stat":  func(m map[string]any, k string) any { return m[k] },
	"thumb": func(m map[string]template.URL, ref string) template.URL { return m[ref] },
}).Parse(reportPage))

// Report is the run as a self-contained HTML page: per-preset budgets, per-stage tables and
// each image with a thumbnail of its input, readable offline.
func (b *Bench) Report(res *Result) ([]byte, error) {
	thumbs := map[string]template.URL{}
	for _, j := range res.Jobs {
		if _, done := thumbs[j.Input]; done || j.Input == "" || b.Store == nil {
			continue
		}
		thumbs[j.Input] = ""
		if path, err := b.Store.Path(j.Input); err == nil {
			if uri, err := thumbnail(path, 96); err == nil {
				thumbs[j.Input] = uri
			}
		}
	}
	var out bytes.Buffer
	err := reportTemplate.Execute(&out, map[string]any{"R": res, "Thumbs": thumbs})
	return out.Bytes(), err
}

// thumbnail scales an image down to size pixels on its long side, averaging the pixels each
// thumbnail pixel covers, as a PNG data URI.
func thumbnail(path string, size int) (template.URL, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	r := src.Bounds()
	w, h := r.Dx(), r.Dy()
	scale := min(1, float64(size)/float64(max(w, h)))
	tw, th := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		y0, y1 := r.Min.Y+y*h/th, r.Min.Y+max((y+1)*h/th, y*h/th+1)
		for x := range tw {
			x0, x1 := r.Min.X+x*w/tw, r.Min.X+max((x+1)*w/tw, x*w/tw+1)
			var sr, sg, sb, sa, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					sr, sg, sb, sa, n = sr+uint64(cr), sg+uint64(cg), sb+uint64(cb), sa+uint64(ca), n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(sr / n >> 8), uint8(sg / n >> 8), uint8(sb / n >> 8), uint8(sa / n >> 8)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return "", err
	}
	// The page carries its pictures, so it reads offline and travels as one file.
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())), nil
}
