package spec

const (
	AgentConfigLayerMediatype = "application/vnd.openotters.agent.config.v1+json"
	ContextLayerMediaType     = "application/vnd.openotters.context.v1"
	AgentArtifactType         = "application/vnd.openotters.agent.v1"

	// BinArtifactType marks an OCI image as an openotters bin-tool
	// (vnd.openotters.bin.* annotations, single binary per platform).
	// Lets consumers distinguish tool images from agent images without
	// relying on annotation-sniffing.
	BinArtifactType = "application/vnd.openotters.bin.v1"

	OctetStream = "application/octet-stream"
	Markdown    = "text/markdown"

	AnnotationBinName        = "vnd.openotters.bin.name"
	AnnotationBinPath        = "vnd.openotters.bin.path"
	AnnotationBinDescription = "vnd.openotters.bin.description"
	AnnotationBinUsage       = "vnd.openotters.bin.usage"

	// LabelArtifactType is stamped on the OCI image config Labels
	// at build time so consumers can read the openotters artifact
	// kind (`vnd.openotters.{agent,bin}.v1`) via cli.ImageInspect's
	// Config.Labels in a single cheap roundtrip — without pulling
	// the manifest blob via ImageSave (which streams the entire
	// image tar). The same value is also present as the manifest's
	// own ArtifactType field, but that field isn't surfaced by
	// docker's ImageInspect API. Build pipelines must populate
	// both for compatibility: ArtifactType for OCI-aware consumers
	// (oras, registries), and this label for daemon-side fast paths.
	LabelArtifactType = "io.openotters.artifact-type"

	DefaultBinPath   = "/"
	DefaultUsagePath = "/USAGE.md"
)
