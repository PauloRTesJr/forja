package fal

import (
	"regexp"
	"slices"
	"strings"

	"forja/internal/engineapi"
)

// Spec is how Forja calls one fal endpoint, read from its OpenAPI schema: which input fields
// take the image (and the mesh, for retexturing), which output field holds the mesh, and the
// switches set so the model returns a textured GLB. No endpoint needs code of its own.
type Spec struct {
	Endpoint string
	// Image takes the front view; with ImageList it is a list, and the other views follow the
	// front in it.
	Image     string
	ImageList bool
	// Views are the fields of the other views it reads ("back" -> "back_image_url"), and
	// ViewsRequired the ones it cannot run without.
	Views         map[string]string
	ViewsRequired []string
	Mesh          string
	// Output is a dotted path to a File in the result: "model_glb", "model_urls.glb".
	Output string
	Seed   string
	Fixed  map[string]any
	// Tasks are the Forja stage types the endpoint does: shape and texture for image-to-3D,
	// texture alone when it also takes a mesh.
	Tasks []string
	// Problem says why Forja cannot call the endpoint; empty when it can.
	Problem string
}

var (
	imageFields = []string{"image_url", "input_image_url"}
	meshField   = regexp.MustCompile(`^(input_)?(mesh|model)(_file)?_url$`)
	// Switches that would otherwise return an untextured mesh, or another format.
	textureSwitches = []string{"texture", "textured_mesh", "should_texture"}
	outputPreferred = []string{"model_glb", "glb", "model_mesh", "mesh", "pbr_model", "model"}
)

// MapSchema reads an endpoint's OpenAPI 3.0 document as fal's model API returns it.
func MapSchema(endpoint string, api map[string]any) Spec {
	spec := Spec{Endpoint: endpoint, Fixed: map[string]any{}}
	schemas, _ := dig(api, "components", "schemas").(map[string]any)
	input, output := requestSchemas(api, schemas)
	if input == nil || output == nil {
		spec.Problem = "its API description has no input or output"
		return spec
	}
	props, _ := input["properties"].(map[string]any)
	required := stringsOf(input["required"])

	for _, name := range append(slices.Clone(imageFields), "front_image_url") {
		if t := typeOf(props[name], schemas); t == "string" {
			spec.Image = name
			break
		}
	}
	if spec.Image == "" && typeOf(props["image_urls"], schemas) == "array" {
		spec.Image, spec.ImageList = "image_urls", true
	}
	if spec.Image == "" {
		spec.Problem = "it does not take a single image"
		return spec
	}
	// Multi-view endpoints name a field per view: front_image_url, back_image_url and so on.
	views := map[string]string{} // field -> view
	for _, v := range engineapi.Views[1:] {
		if name := v + "_image_url"; typeOf(props[name], schemas) == "string" {
			if spec.Views == nil {
				spec.Views = map[string]string{}
			}
			spec.Views[v], views[name] = name, v
		}
	}
	for name := range props {
		if meshField.MatchString(name) && typeOf(props[name], schemas) == "string" {
			spec.Mesh = name
		}
	}
	for _, name := range required {
		switch {
		case name == spec.Image || name == spec.Mesh:
		case views[name] != "":
			spec.ViewsRequired = append(spec.ViewsRequired, views[name])
		default:
			spec.Problem = "it needs " + name + ", which Forja cannot fill"
			return spec
		}
	}
	slices.SortFunc(spec.ViewsRequired, func(a, b string) int {
		return slices.Index(engineapi.Views, a) - slices.Index(engineapi.Views, b)
	})
	for _, name := range []string{"seed", "model_seed"} {
		if typeOf(props[name], schemas) == "integer" {
			spec.Seed = name
			break
		}
	}
	for _, name := range textureSwitches {
		if typeOf(props[name], schemas) == "boolean" {
			spec.Fixed[name] = true
		}
	}
	if f, ok := props["geometry_file_format"].(map[string]any); ok && slices.Contains(stringsOf(f["enum"]), "glb") {
		spec.Fixed["geometry_file_format"] = "glb"
	}

	spec.Output = meshOutput(output, schemas)
	if spec.Output == "" {
		spec.Problem = "it returns no GLB mesh"
		return spec
	}
	if spec.Mesh != "" {
		spec.Tasks = []string{"texture"}
	} else {
		spec.Tasks = []string{"shape", "texture"}
	}
	return spec
}

// requestSchemas follows the POST body and the request result to their schemas.
func requestSchemas(api, schemas map[string]any) (input, output map[string]any) {
	paths, _ := api["paths"].(map[string]any)
	for path, ops := range paths {
		ops, _ := ops.(map[string]any)
		if post, ok := ops["post"].(map[string]any); ok {
			input = resolve(dig(post, "requestBody", "content", "application/json", "schema"), schemas)
		}
		if get, ok := ops["get"].(map[string]any); ok && strings.HasSuffix(path, "/requests/{request_id}") {
			output = resolve(dig(get, "responses", "200", "content", "application/json", "schema"), schemas)
		}
	}
	return input, output
}

// meshOutput finds the File (or the URL bundle holding a GLB) the mesh comes back in.
func meshOutput(output, schemas map[string]any) string {
	props, _ := output["properties"].(map[string]any)
	files := map[string]bool{}
	bundles := map[string]map[string]any{}
	for name, p := range props {
		target := resolve(p, schemas)
		if isFile(target) {
			files[name] = true
		} else if inner, ok := target["properties"].(map[string]any); ok {
			bundles[name] = inner
		}
	}
	for _, name := range outputPreferred {
		if files[name] {
			return name
		}
	}
	for name := range files {
		if strings.Contains(name, "glb") || strings.Contains(name, "mesh") {
			return name
		}
	}
	for name, inner := range bundles {
		if isFile(resolve(inner["glb"], schemas)) {
			return name + ".glb"
		}
	}
	return ""
}

func isFile(s map[string]any) bool {
	props, _ := s["properties"].(map[string]any)
	_, url := props["url"]
	_, size := props["file_size"]
	_, ctype := props["content_type"]
	return url && (size || ctype)
}

// resolve follows a $ref, directly or in the first anyOf branch that has one.
func resolve(node any, schemas map[string]any) map[string]any {
	n, _ := node.(map[string]any)
	if n == nil {
		return nil
	}
	if ref, ok := n["$ref"].(string); ok {
		target, _ := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
		return target
	}
	for _, branch := range anySlice(n["anyOf"]) {
		if b, ok := branch.(map[string]any); ok && b["$ref"] != nil {
			return resolve(b, schemas)
		}
	}
	return n
}

// typeOf is a property's JSON type, looking through anyOf with null.
func typeOf(node any, schemas map[string]any) string {
	n, _ := node.(map[string]any)
	if n == nil {
		return ""
	}
	if t, ok := n["type"].(string); ok {
		return t
	}
	for _, branch := range anySlice(n["anyOf"]) {
		if b, ok := branch.(map[string]any); ok {
			if t, _ := b["type"].(string); t != "" && t != "null" {
				return t
			}
		}
	}
	if _, ok := n["$ref"]; ok {
		t, _ := resolve(n, schemas)["type"].(string)
		return t
	}
	return ""
}

func dig(node any, keys ...string) any {
	for _, k := range keys {
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = m[k]
	}
	return node
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func stringsOf(v any) []string {
	var out []string
	for _, x := range anySlice(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
