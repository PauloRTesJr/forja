package quality

import (
	"slices"
	"testing"
)

func TestEachMetricIsWordedForPeople(t *testing.T) {
	for _, c := range []struct {
		key   string
		value float64
		words string
		cell  string
	}{
		{"match_iou", 0.93, "reference fit 93%", "93%"},
		{"match_reprojected", 0.41, "reference projected onto 41% of the texture", "41%"},
		{"match_left_iou", 0.62, "left view fit 62%", "62%"},
		{"match_back_iou", 0.5, "back view fit 50%", "50%"},
		{"bake_hit_ratio", 0.99, "bake reached the high-poly on 99% of texels", "99%"},
		{"bake_cage", 0.05, "bake cage 5.0% of the size", "5.0%"},
		{"uv_coverage", 0.71, "UV space used 71%", "71%"},
		{"lod_open_edges", 0, "LOD cracks: 0 open edges", "0"},
		{"lod_open_edges", 1, "LOD cracks: 1 open edge", "1"},
		{"lod_open_edges", 137, "LOD cracks: 137 open edges", "137"},
		{"something_new", 3, "something_new 3", "3"},
		{"something_new", 0.25, "something_new 0.25", "0.25"},
	} {
		if got := Words(c.key, c.value); got != c.words {
			t.Errorf("Words(%s, %v) = %q, want %q", c.key, c.value, got, c.words)
		}
		if got := Value(c.key, c.value); got != c.cell {
			t.Errorf("Value(%s, %v) = %q, want %q", c.key, c.value, got, c.cell)
		}
	}
}

func TestLinesFollowTheTableWithSideViewsAfterTheFrontFitAndUnknownKeysLast(t *testing.T) {
	got := Lines(map[string]float64{
		"zeta": 1, "alpha": 2, "uv_coverage": 0.7, "match_right_iou": 0.8, "match_iou": 0.9,
		"match_left_iou": 0.6, "match_reprojected": 0.4,
	})
	want := []string{
		"reference fit 90%", "left view fit 60%", "right view fit 80%", "reference projected onto 40% of the texture",
		"UV space used 70%", "alpha 2", "zeta 1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if Lines(nil) != nil {
		t.Fatal("no metrics, no lines")
	}
}

func TestOnlyKnownMetricsHaveADirection(t *testing.T) {
	for key, want := range map[string]Direction{
		"match_iou": HigherIsBetter, "match_reprojected": HigherIsBetter, "match_left_iou": HigherIsBetter,
		"bake_hit_ratio": HigherIsBetter, "uv_coverage": HigherIsBetter, "lod_open_edges": FewerIsBetter,
		"bake_cage": Informative, "mystery": Informative, "match_a_b_iou": Informative,
	} {
		if got := DirectionOf(key); got != want {
			t.Errorf("DirectionOf(%s) = %v, want %v", key, got, want)
		}
	}
}

func TestSharesRegressPastThePercentAndTheAbsoluteFloorAndCrackCountsOnAnyGrowth(t *testing.T) {
	for _, c := range []struct {
		name          string
		key           string
		base, current float64
		want          bool
	}{
		{"a clear drop", "match_iou", 0.93, 0.70, true},
		{"within the percent", "match_iou", 0.93, 0.88, false},
		{"past the percent, under the floor", "uv_coverage", 0.05, 0.045, false},
		{"just past both", "uv_coverage", 0.50, 0.44, true},
		{"a rise", "bake_hit_ratio", 0.9, 0.99, false},
		{"from nothing", "match_reprojected", 0, 0, false},
		{"a side view", "match_left_iou", 0.9, 0.5, true},
		{"one more crack", "lod_open_edges", 0, 1, true},
		{"fewer cracks", "lod_open_edges", 5, 0, false},
		{"as many cracks", "lod_open_edges", 5, 5, false},
		{"a cage that moved", "bake_cage", 0.05, 0.5, false},
		{"a key nobody knows", "mystery", 1, 0, false},
	} {
		if got := Regressed(c.key, c.base, c.current, 10); got != c.want {
			t.Errorf("%s: Regressed(%s, %v, %v) = %v, want %v", c.name, c.key, c.base, c.current, got, c.want)
		}
	}
}
