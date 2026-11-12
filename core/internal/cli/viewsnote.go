package cli

import (
	"fmt"
	"slices"
	"strings"

	"forja/internal/jobs"
)

// unreadNote says, for a stage whose model does not read some of the job's views, that they
// will not change its result, and which model would read them. "" when the stage reads them all.
// The app says the same in lib/viewsNotice.ts.
func unreadNote(p jobs.Pick) string {
	if len(p.UnreadViews) == 0 {
		return ""
	}
	verb, what := "works", "the result"
	switch {
	case p.Use == "shape":
		verb, what = "shapes", "the shape"
	case p.Use == "texture" || strings.HasPrefix(p.Use, "texture."):
		verb, what = "paints", "the texture"
	}
	model := orDash(p.ModelName, p.Model)
	var lead string
	if slices.ContainsFunc(p.Views, func(v string) bool { return v != "front" }) {
		lead = fmt.Sprintf("%s does not read %s: %s will not change %s.", model, theViews(p.UnreadViews),
			oneOrMany(p.UnreadViews, "it", "they"), what)
	} else {
		lead = fmt.Sprintf("%s %s from the front alone: %s will not change %s.", model, verb, theViews(p.UnreadViews), what)
	}
	rb := p.ReadBy
	if rb == nil {
		return lead
	}
	reads := slices.DeleteFunc(slices.Clone(p.UnreadViews), func(v string) bool { return !slices.Contains(rb.Reads, v) })
	object := theViews(reads)
	if len(reads) == len(p.UnreadViews) {
		object = oneOrMany(reads, "it", "them")
	}
	by := orDash(rb.ModelName, rb.Model)
	if rb.Installed {
		return fmt.Sprintf("%s %s reads %s, but cannot run on %s yet.", lead, by, object, orDash(rb.ProviderLabel, rb.InstallOn))
	}
	install := "forja models install " + rb.Model
	if rb.InstallOn != jobs.LocalProvider {
		install += " --provider " + rb.InstallOn
	}
	return fmt.Sprintf("%s %s reads %s (%s).", lead, by, object, install)
}

// theViews names views as people list them: "the left view", "the left and back views",
// "the left, back and right views".
func theViews(views []string) string {
	list := strings.Join(views, "")
	if len(views) > 1 {
		list = strings.Join(views[:len(views)-1], ", ") + " and " + views[len(views)-1]
	}
	return "the " + list + oneOrMany(views, " view", " views")
}

// oneOrMany is one word for a single view and another for several.
func oneOrMany(views []string, one, many string) string {
	if len(views) == 1 {
		return one
	}
	return many
}
