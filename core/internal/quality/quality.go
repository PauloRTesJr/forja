// Package quality words the numbers stages measure about their outputs (engine API 1.9), and
// says which way is better for each. The CLI, the benchmark and the tests read this one table;
// the app's lib/quality.ts mirrors its wording.
package quality

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Direction says which way a metric moves when the output gets better.
type Direction int

const (
	// Informative metrics are shown and never gated on.
	Informative Direction = iota
	// HigherIsBetter metrics are shares between 0 and 1 (a fit, a coverage).
	HigherIsBetter
	// FewerIsBetter metrics count flaws.
	FewerIsBetter
)

// MinDrop is the smallest absolute drop of a HigherIsBetter metric that counts as a regression,
// whatever the percent: a share that slips from 0.50 to 0.495 is rounding, not a worse asset.
const MinDrop = 0.01

// Views are the side views texture.match can fit, in the order they are shown.
var Views = []string{"left", "back", "right"}

type kind struct {
	key   string
	dir   Direction
	words func(v float64) string
	// value is the number alone, as a delta table cell shows it.
	value func(v float64) string
}

// share rounds halves up, as the app does.
func share(v float64) string { return fmt.Sprintf("%.0f%%", math.Round(v*100)) }

func count(v float64) string { return fmt.Sprintf("%.0f", v) }

// table is the wording of every metric the built-in stages emit, in the order lines show them.
var table = []kind{
	{"match_iou", HigherIsBetter, func(v float64) string { return "reference fit " + share(v) }, share},
	{"match_reprojected", HigherIsBetter, func(v float64) string {
		return "reference projected onto " + share(v) + " of the texture"
	}, share},
	{"bake_hit_ratio", HigherIsBetter, func(v float64) string {
		return "bake reached the high-poly on " + share(v) + " of texels"
	}, share},
	{"bake_cage", Informative, func(v float64) string {
		return fmt.Sprintf("bake cage %.1f%% of the size", v*100)
	}, func(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }},
	{"uv_coverage", HigherIsBetter, func(v float64) string { return "UV space used " + share(v) }, share},
	{"lod_open_edges", FewerIsBetter, func(v float64) string {
		return fmt.Sprintf("LOD cracks: %s open %s", count(v), plural(v, "edge"))
	}, count},
}

func plural(v float64, noun string) string {
	if v == 1 {
		return noun
	}
	return noun + "s"
}

// sideView reads "match_left_iou" as the view "left".
func sideView(key string) (string, bool) {
	view, ok := strings.CutPrefix(key, "match_")
	if !ok {
		return "", false
	}
	view, ok = strings.CutSuffix(view, "_iou")
	return view, ok && view != "" && !strings.Contains(view, "_")
}

func find(key string) (kind, bool) {
	for _, k := range table {
		if k.key == key {
			return k, true
		}
	}
	if view, ok := sideView(key); ok {
		return kind{key, HigherIsBetter, func(v float64) string { return view + " view fit " + share(v) }, share}, true
	}
	return kind{}, false
}

// Words is one metric as a person reads it: "reference fit 93%". A key the table does not know
// reads "key value".
func Words(key string, v float64) string {
	if k, ok := find(key); ok {
		return k.words(v)
	}
	return key + " " + number(v)
}

// Value is the number alone, in the unit the metric uses: "93%", "0".
func Value(key string, v float64) string {
	if k, ok := find(key); ok {
		return k.value(v)
	}
	return number(v)
}

func number(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.4g", v)
}

// DirectionOf says which way is better for key; Informative for a key the table does not know.
func DirectionOf(key string) Direction {
	k, _ := find(key)
	return k.dir
}

// Keys are the keys of m in the order lines show them: the table's, with the side views right
// after the front view's fit, then the rest by name.
func Keys(m map[string]float64) []string {
	var known, views, rest []string
	for key := range m {
		switch {
		case isSideView(key):
			views = append(views, key)
		case slices.ContainsFunc(table, func(k kind) bool { return k.key == key }):
			known = append(known, key)
		default:
			rest = append(rest, key)
		}
	}
	index := func(key string) int { return slices.IndexFunc(table, func(k kind) bool { return k.key == key }) }
	slices.SortFunc(known, func(a, b string) int { return index(a) - index(b) })
	slices.SortFunc(views, func(a, b string) int {
		va, _ := sideView(a)
		vb, _ := sideView(b)
		ia, ib := slices.Index(Views, va), slices.Index(Views, vb)
		if ia < 0 {
			ia = len(Views)
		}
		if ib < 0 {
			ib = len(Views)
		}
		if ia != ib {
			return ia - ib
		}
		return strings.Compare(a, b)
	})
	slices.Sort(rest)
	out := make([]string, 0, len(m))
	for _, key := range known {
		out = append(out, key)
		if key == "match_iou" {
			out, views = append(out, views...), nil
		}
	}
	return append(append(out, views...), rest...)
}

func isSideView(key string) bool {
	_, ok := sideView(key)
	return ok
}

// Lines words every metric of m, one line each, in the order of Keys.
func Lines(m map[string]float64) []string {
	var lines []string
	for _, key := range Keys(m) {
		lines = append(lines, Words(key, m[key]))
	}
	return lines
}

// Regressed says whether current is a worse value of key than baseline: a HigherIsBetter share
// that fell by more than pct percent of the baseline and by more than MinDrop, or a count of
// flaws that grew at all. Informative metrics never regress.
func Regressed(key string, baseline, current, pct float64) bool {
	switch DirectionOf(key) {
	case HigherIsBetter:
		drop := baseline - current
		return baseline > 0 && drop > MinDrop && drop/baseline*100 > pct
	case FewerIsBetter:
		return current > baseline
	}
	return false
}
