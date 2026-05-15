package system

import (
	"context"

	"github.com/go-git/go-billy/v6"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/model"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// MaterializeOptions packages the inputs MaterializeContent needs.
// Fields mirror the system workspace's internal struct so the
// executor that lives elsewhere (today: the Docker executor) can
// drive the same materialise pipeline without re-implementing it.
type MaterializeOptions struct {
	Store          oras.ReadOnlyTarget
	Ref            spec.Reference
	Overrides      []spec.Override
	OCIPuller      agentoci.Puller
	UsageFetcher   agentoci.UsageFetcher
	ModelResolver  model.Resolver
	DigestResolver DigestResolver
	ImageRef       string
	LocalRuntime   string
	Mounts         []executor.Mount
	HostFS         billy.Filesystem
	// ToolBinaryPath optionally returns an absolute in-container
	// path for tool `name`. The docker executor returns
	// `/opt/bins/<name>` so the runtime resolves the symlink
	// stamped on the agent root (which the kernel then follows to
	// the read-only image-mount, hidden under /opt/bin-images).
	ToolBinaryPath func(name string) string

	// SymlinkBinAt, when non-nil, is invoked once per declared BIN
	// with (name, target) — name is the BIN's tool name (resolves
	// to opt/bins/<name> on the agent root), target is the
	// container-local string the symlink should point at (e.g.
	// /opt/bin-images/<name>/<name>). Only the docker executor sets
	// this; system installs the binary as a regular file under
	// usr/bin and doesn't need the indirection.
	SymlinkBinAt func(name string) (target string, ok bool)

	// Capabilities is the list of LLM-facing tool functions the
	// runtime image registers, each with a description. Surfaces
	// in agent.yaml's capabilities: block. Daemon-supplied — the
	// agentfile library itself doesn't know which tools exist or
	// what they do.
	Capabilities []executor.Capability

	// View is the agent-visible filesystem layout used to render
	// WORKSPACE.md. Zero-value uses system defaults derived from
	// fs.Root() (host path, no sandbox). The docker executor sets
	// a container-rooted view so the LLM doesn't see the host
	// path of its bind mount.
	View executor.WorkspaceView

	// ViewBinDirsForTools, when non-nil, fills View.BinDirs from
	// the resolved tool name list — invoked once after the spec is
	// parsed and tools are populated, before WORKSPACE.md is
	// rendered. Lets the docker executor produce one
	// `/opt/bins/<name>` entry per BIN without parsing the spec
	// twice.
	ViewBinDirsForTools func(toolNames []string) []string
}

// MaterializeContent runs the system executor's content-only
// materialisation pipeline against fs: pulls the agent image,
// extracts CONTEXT + ADD layers, generates AGENT.md / WORKSPACE.md
// / agent.yaml, resolves model credentials, applies user mount
// symlinks. Does NOT install runtime / BIN binaries to disk —
// that step is system-only.
//
// Exposed for the Docker executor to reuse the same FHS layout +
// metadata generation without copying binaries the container will
// pick up via image mounts instead.
//
// Errors join ErrPull / ErrModel where appropriate so callers can
// route them to the right Status (StatusPullError /
// StatusModelError).
func MaterializeContent(
	ctx context.Context,
	fs billy.Filesystem,
	id uuid.UUID,
	addr string,
	opts MaterializeOptions,
) (*executor.Runtime, error) {
	w := &workspace{
		store:               opts.Store,
		ref:                 opts.Ref,
		overrides:           opts.Overrides,
		ociPuller:           opts.OCIPuller,
		usageFetcher:        opts.UsageFetcher,
		modelResolver:       opts.ModelResolver,
		digestResolver:      opts.DigestResolver,
		imageRef:            opts.ImageRef,
		localRuntime:        opts.LocalRuntime,
		mounts:              opts.Mounts,
		toolBinaryPath:      opts.ToolBinaryPath,
		symlinkBinAt:        opts.SymlinkBinAt,
		capabilities:        opts.Capabilities,
		view:                opts.View,
		viewBinDirsForTools: opts.ViewBinDirsForTools,
		hostFS:              opts.HostFS,
	}

	rt, _, err := w.materializeContent(ctx, fs, id, addr)
	if err != nil {
		return nil, err
	}

	return rt, nil
}
