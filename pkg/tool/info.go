package tool

import v1 "github.com/opencontainers/image-spec/specs-go/v1"

const (
	defaultBinName = "bin"

	AnnotationToolBin         = "vnd.openotters.tool.bin"
	AnnotationToolDescription = "vnd.openotters.tool.description"
	AnnotationToolUsage       = "vnd.openotters.tool.usage"
)

// LayerInfo describes a single layer in the tool image.
type LayerInfo struct {
	Title     string
	MediaType string
	Size      int64
	Digest    string
}

// ToolInfo holds metadata extracted from a tool manifest.
type ToolInfo struct { //nolint:revive // public API
	BinPath     string
	Description string
	UsagePath   string
	Layers      []LayerInfo
}

// Info reads tool metadata from manifest annotations and layers.
// BinPath defaults to "bin" if vnd.openotters.tool.bin is absent.
func Info(manifest v1.Manifest) ToolInfo {
	info := ToolInfo{BinPath: defaultBinName}

	if v, ok := manifest.Annotations[AnnotationToolBin]; ok && v != "" {
		info.BinPath = v
	}

	if v, ok := manifest.Annotations[AnnotationToolDescription]; ok {
		info.Description = v
	}

	if v, ok := manifest.Annotations[AnnotationToolUsage]; ok {
		info.UsagePath = v
	}

	for _, l := range manifest.Layers {
		info.Layers = append(info.Layers, LayerInfo{
			Title:     l.Annotations[v1.AnnotationTitle],
			MediaType: l.MediaType,
			Size:      l.Size,
			Digest:    l.Digest.String(),
		})
	}

	return info
}
