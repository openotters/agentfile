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
	// (single binary per platform, io.openotters.bin.* annotations
	// for openotters-specific metadata, OCI image-spec keys for
	// everything covered by the spec). Lets consumers distinguish
	// tool images from agent images without annotation-sniffing.
	BinArtifactType = "application/vnd.openotters.bin.v1"

	OctetStream = "application/octet-stream"
	Markdown    = "text/markdown"

	// AnnotationBinName is the binary's filename inside the tar
	// layer — what the puller looks up when extracting the bin
	// from the rootfs. Distinct from org.opencontainers.image.title,
	// which is the human-readable display label for the image
	// ("jq command-line JSON processor") and may differ from the
	// binary filename ("jq"). Reverse-DNS form per the OCI
	// image-spec custom-key rule.
	AnnotationBinName = "io.openotters.bin.name"

	// AnnotationBinPath is the in-image absolute path the daemon
	// binds into the agent filesystem when the bin is mounted. No
	// OCI predefined key covers this — it's an openotters runtime
	// concept (where the daemon mounts the binary), not image
	// metadata an external tool would care about.
	AnnotationBinPath = "io.openotters.bin.path"

	// AnnotationBinUsage is the in-image path to a markdown file
	// describing how the model should invoke this bin. Loaded into
	// the agent's system prompt at run time. Distinct from
	// org.opencontainers.image.documentation (which is a URL).
	AnnotationBinUsage = "io.openotters.bin.usage"

	DefaultBinPath   = "/"
	DefaultUsagePath = "/USAGE.md"
)
