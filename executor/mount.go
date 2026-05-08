package executor

// Mount is a host-path → in-agent binding declared by the user via
// `otters run -v HOST:TARGET[:DESC]`. Both the system executor (which
// realises mounts as symlinks inside the chrooted agent root) and the
// Docker executor (which realises them as bind-mounts inside the
// container) use this type.
//
// Host must be an absolute host path. Target is the path the agent
// sees — system maps it under the chroot root, Docker maps it as the
// container-side bind target. Description is optional and surfaces to
// the LLM via the generated MOUNTS.md context layer.
type Mount struct {
	Host        string
	Target      string
	Description string
}
