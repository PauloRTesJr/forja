package export

import "slices"

// Format is a file format the library exports assets to (docs/decisions.md, decision 19).
type Format struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Ext is the extension of the main file; some formats write their textures beside it.
	Ext string `json:"ext"`
	// For says who reads the format and what it keeps of the asset.
	For string `json:"for"`
	// Blender is true when Blender makes the file from the asset's GLB.
	Blender bool `json:"blender"`
}

var formats = []Format{
	{ID: "glb", Name: "GLB", Ext: ".glb", For: "The asset as made, with its LODs: Unity, Godot, Unreal and the web"},
	{ID: "fbx", Name: "FBX", Ext: ".fbx", For: "Unity, Unreal, Maya and 3ds Max, textures inside; the LODs become a LOD group", Blender: true},
	{ID: "obj", Name: "OBJ", Ext: ".obj", For: "Any 3D tool; full detail only, its material and textures beside it", Blender: true},
	{ID: "gltf", Name: "glTF", Ext: ".gltf", For: "The GLB as separate JSON, buffer and texture files", Blender: true},
	{ID: "usdz", Name: "USDZ", Ext: ".usdz", For: "AR Quick Look on iPhone, iPad and Vision Pro; full detail only", Blender: true},
	{ID: "stl", Name: "STL", Ext: ".stl", For: "3D printing, in millimetres; the shape only, at full detail", Blender: true},
	{ID: "blend", Name: "Blender file", Ext: ".blend", For: "A .blend with the textures packed in and the lower LODs hidden", Blender: true},
}

// Formats lists every export format, GLB first.
func Formats() []Format { return slices.Clone(formats) }

// LookupFormat finds a format by its id, such as "fbx".
func LookupFormat(id string) (Format, bool) {
	i := slices.IndexFunc(formats, func(f Format) bool { return f.ID == id })
	if i < 0 {
		return Format{}, false
	}
	return formats[i], true
}
