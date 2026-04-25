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

	DefaultBinPath   = "/"
	DefaultUsagePath = "/USAGE.md"
)
