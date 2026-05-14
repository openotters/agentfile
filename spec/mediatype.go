package spec

const (
	AgentConfigLayerMediatype = "application/vnd.openotters.agent.config.v1+json"
	ContextLayerMediaType     = "application/vnd.openotters.context.v1"
	AgentArtifactType         = "application/vnd.openotters.agent.v1"

	// AgentfileMediaType marks the layer carrying the raw,
	// user-authored Agentfile bytes — verbatim, not a
	// marshal/reconstruct of the parsed spec. Build pipelines push
	// this alongside the context / add layers; materialisation
	// extracts it to <agent-root>/etc/Agentfile so the image stays
	// self-describing in a form an operator can read or re-build
	// from without a registry round-trip. The Agentfile is the
	// source of truth for the agent; this mediatype represents that
	// source as it was written. No version suffix: spec version
	// belongs in the SYNTAX directive inside the file itself, not
	// in the wire mediatype.
	AgentfileMediaType = "application/vnd.openotters.agentfile"

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
