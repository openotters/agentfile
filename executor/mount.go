package executor

// Mount is a host-path → in-agent binding declared by the user via
// `otters run -v HOST:TARGET[:DESC][:ro|:rw]`. Both the system
// executor (which realises mounts as symlinks inside the chrooted
// agent root) and the Docker executor (which realises them as
// bind-mounts inside the container) use this type.
//
// Host must be an absolute host path. Target is the path the agent
// sees — system maps it under the chroot root, Docker maps it as the
// container-side bind target. Description is optional and surfaces
// to the LLM via the generated MOUNTS.md context layer.
//
// ReadOnly maps to docker's bind ReadOnly flag on the docker
// executor; the system executor doesn't enforce it (no sandbox), but
// surfaces it in MOUNTS.md so the model knows the user's intent.
type Mount struct {
	Host        string
	Target      string
	Description string
	ReadOnly    bool
}
