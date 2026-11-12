package presets

import "testing"

func TestOnlyStagesThatReadThePreparedImageTakeViews(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := all["environment-piece"].Active()
	want := map[string]bool{"shape": true, "paint": true, "bg": false, "frame": false, "cleanup": false, "bake": false}
	for id, takes := range want {
		if got := p.TakesViews(id); got != takes {
			t.Errorf("%s takes views = %v, want %v", id, got, takes)
		}
	}
	if p.TakesViews("no-such-stage") {
		t.Error("an unknown stage takes views")
	}
}
