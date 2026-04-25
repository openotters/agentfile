package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/agent"
	"github.com/openotters/agentfile/model"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
)

// Path constants for the agent workspace filesystem layout.
// They use filepath.Join (cross-platform separators), so they can't be
// declared as `const` — Go const requires compile-time literals.
//
//nolint:gochecknoglobals // logically constants; filepath.Join forbids const
var (
	runtimeBinDir = filepath.Join("usr", "local", "bin")
	RuntimeBin    = filepath.Join(runtimeBinDir, "runtime")
	contextDir    = filepath.Join("etc", "context")
	dataDir       = filepath.Join("etc", "data")
	binDir        = filepath.Join("usr", "bin")

	fhsDirs = []string{
		contextDir,
		dataDir,
		binDir,
		runtimeBinDir,
		"workspace",
		"tmp",
		filepath.Join("var", "lib"),
	}
)

// ErrPull indicates the agent image could not be loaded from the OCI store.
var ErrPull = fmt.Errorf("agent pull error")

// workspace holds everything needed to materialize an agent workspace.
type workspace struct {
	store         oras.ReadOnlyTarget
	ref           spec.Reference
	overrides     []spec.Override
	ociPuller     agentoci.Puller
	modelResolver model.Resolver
	localRuntime  string
	mounts        []Mount
	// hostFS is a non-chrooted filesystem used for operations on real
	// host paths: the mount symlink target, the local-runtime source
	// file. NewAgent defaults to osfs.New("/"); tests substitute memfs.
	// Distinct from fs (the per-agent chroot) because billy's chroot
	// would rewrite absolute symlink targets and host-file sources.
	hostFS billy.Filesystem
}

func (w *workspace) materialize(
	ctx context.Context, fs billy.Filesystem, id uuid.UUID, addr string,
) (*agent.AgentRuntime, error) {
	manifest, af, err := afstore.Load(ctx, w.store, w.ref)
	if err != nil {
		return nil, errors.Join(ErrPull, fmt.Errorf("loading %s: %w", w.ref, err))
	}

	af.Apply(w.overrides...)

	for _, dir := range fhsDirs {
		if mkdirErr := fs.MkdirAll(dir, 0o755); mkdirErr != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, mkdirErr)
		}
	}

	if e := w.extractLayers(ctx, fs, manifest); e != nil {
		return nil, e
	}

	if e := w.installRuntime(ctx, fs, af); e != nil {
		return nil, e
	}

	if e := w.installTools(ctx, fs, af); e != nil {
		return nil, e
	}

	agentMD := spec.GenerateAgentMD(af)
	if e := util.WriteFile(fs, filepath.Join(contextDir, "AGENT.md"), []byte(agentMD), 0o644); e != nil {
		return nil, fmt.Errorf("writing AGENT.md: %w", e)
	}

	rt := &agent.AgentRuntime{
		ID:     id,
		Source: af,
		ResolvedConfig: agent.ResolvedConfig{
			Name:  af.Agent.Name,
			Model: af.Agent.Model,
			Addr:  addr,
			Exec:  af.Agent.Exec,
		},
	}

	// Resolve model API credentials.
	if w.modelResolver != nil {
		apiURL, apiKey, resolveErr := w.modelResolver(af.Agent.Model)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolving model: %w", resolveErr)
		}

		rt.APIBase = apiURL
		rt.APIKey = apiKey
	}

	for _, t := range af.Agent.Bins {
		rt.Tools = append(rt.Tools, agent.ResolvedTool{
			Name:        t.Name,
			Description: t.Description,
			Binary:      filepath.Join(binDir, t.Name),
		})
	}

	if e := rt.WriteTo(fs); e != nil {
		return nil, fmt.Errorf("writing runtime config: %w", e)
	}

	if e := w.writeWorkspaceContext(fs); e != nil {
		return nil, e
	}

	if e := w.applyMounts(fs); e != nil {
		return nil, e
	}

	return rt, nil
}

// workspaceContextMarkdown renders the body of WORKSPACE.md given the
// real on-disk root of the agent's workspace. Kept pure so the exact
// phrasing the LLM sees can be regression-tested without exercising
// billy. An empty root omits the "Your workspace on the host" line.
func workspaceContextMarkdown(root string) []byte {
	var b bytes.Buffer

	b.WriteString("# Your workspace\n\n")
	b.WriteString("You run as a subprocess on the user's host machine. ")
	b.WriteString("The openotters daemon has prepared a dedicated directory ")
	b.WriteString("for you with a small FHS-style layout, but **there is no ")
	b.WriteString("chroot or namespace isolation** — tool binaries you invoke ")
	b.WriteString("see the user's real filesystem. Treat absolute paths with ")
	b.WriteString("care and default to the dirs listed below.\n\n")

	if root != "" {
		fmt.Fprintf(&b, "Your workspace on the host: `%s`\n\n", root)
	}

	b.WriteString("Layout inside the workspace:\n\n")
	b.WriteString("- `/etc/context/` — your system-prompt context files " +
		"(this file lives here, alongside AGENT.md and any MOUNTS.md).\n")
	b.WriteString("- `/etc/data/` — static files baked in via `ADD` directives; your subprocess CWD points here.\n")
	b.WriteString("- `/usr/bin/` — your BIN tools (each BIN directive installs one binary here).\n")
	b.WriteString("- `/usr/local/bin/runtime` — the runtime you yourself are.\n")
	b.WriteString("- `/workspace/` — scratch space meant for artefacts you produce during a session.\n")
	b.WriteString("- `/tmp/` — short-lived files.\n")
	b.WriteString("- `/var/lib/` — persistent state (the memory DB lives here).\n\n")

	b.WriteString("Working-directory rules:\n\n")
	b.WriteString("- Commands you run through a BIN inherit the runtime's CWD. Don't rely on it; pass absolute paths.\n")
	b.WriteString("- Prefer writing to `/workspace/` or `/tmp/`; treat `/etc/` and `/usr/` as read-only.\n")
	b.WriteString("- If `/etc/context/MOUNTS.md` exists, those paths are user-mounted host directories — " +
		"the only \"outside\" paths you should explore or modify.\n")
	b.WriteString("- Without a MOUNTS.md entry, do not probe arbitrary host paths " +
		"(`/Users/…`, `/home/…`, `/etc/passwd`) even though they're reachable.\n")

	return b.Bytes()
}

// writeWorkspaceContext drops /etc/context/WORKSPACE.md describing the
// agent's filesystem layout, real host path, and the fact that the
// sandbox is path-prefix scoped (not chroot(2)). Useful because the
// LLM otherwise has no idea what "its" filesystem looks like when a
// tool like `ls` or `pwd` is invoked — and without this note it'll
// happily `ls /` and see the host root.
func (w *workspace) writeWorkspaceContext(fs billy.Filesystem) error {
	return util.WriteFile(fs, filepath.Join(contextDir, "WORKSPACE.md"),
		workspaceContextMarkdown(fs.Root()), 0o644)
}

// applyMounts creates the symlinks declared by WithMounts inside the
// agent's chroot and writes a MOUNTS.md context file so the LLM
// discovers the paths through its normal context-assembly pipeline.
// Idempotent: removing and re-creating a symlink is safe; util.RemoveAll
// on the chroot later removes the links without touching host data.
//
// The symlink operations go through w.hostFS (a non-chrooted billy
// filesystem) rather than the agent's chroot fs, because the chroot
// helper would rewrite absolute symlink targets to live inside the
// chroot root — exactly what we don't want for a bind-mount pointing
// at a real host path. The `link` path is computed from fs.Root() so
// it still lands inside the chroot; the hostFS just provides real,
// non-rewriting symlink semantics.
func (w *workspace) applyMounts(fs billy.Filesystem) error {
	if len(w.mounts) == 0 {
		return nil
	}

	root := fs.Root()
	if root == "" {
		return fmt.Errorf("mount requires a filesystem with a real root directory")
	}

	for _, m := range w.mounts {
		rel := strings.TrimPrefix(m.Target, "/")
		if rel == "" {
			return fmt.Errorf("mount target %q is empty", m.Target)
		}

		link := filepath.Join(root, rel)

		if err := w.hostFS.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return fmt.Errorf("mount %s: %w", m.Target, err)
		}

		_ = w.hostFS.Remove(link)

		if err := w.hostFS.Symlink(m.Host, link); err != nil {
			return fmt.Errorf("mount %s -> %s: %w", m.Target, m.Host, err)
		}
	}

	return w.writeMountsContext(fs)
}

// mountsContextMarkdown renders the body of MOUNTS.md for a given set
// of mount specs. Pure; exists as its own function so the wording the
// LLM sees is regression-testable.
func mountsContextMarkdown(mounts []Mount) []byte {
	var b bytes.Buffer

	b.WriteString("# Host-mounted paths\n\n")
	b.WriteString("The following paths inside your workspace are backed by directories on the user's host machine. ")
	b.WriteString("You can read and write through them; changes are visible to the host immediately. ")
	b.WriteString("Do not assume any other top-level paths are mounted.\n\n")

	for _, m := range mounts {
		fmt.Fprintf(&b, "- `%s`", m.Target)

		if m.Description != "" {
			fmt.Fprintf(&b, " — %s", m.Description)
		}

		b.WriteString("\n")
	}

	return b.Bytes()
}

// writeMountsContext renders /etc/context/MOUNTS.md so the runtime's
// context assembler picks it up alongside the user-authored CONTEXT
// entries. The file is regenerated each materialize — safe to drop and
// rewrite on every Restore.
func (w *workspace) writeMountsContext(fs billy.Filesystem) error {
	return util.WriteFile(fs, filepath.Join(contextDir, "MOUNTS.md"),
		mountsContextMarkdown(w.mounts), 0o644)
}

func (w *workspace) extractLayers(ctx context.Context, fs billy.Filesystem, manifest *v1.Manifest) error {
	for _, layer := range afstore.Layers(manifest, spec.ContextLayerMediaType) {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, err := afstore.FetchLayer(ctx, w.store, layer)
		if err != nil {
			return fmt.Errorf("fetching context %s: %w", title, err)
		}

		if e := util.WriteFile(fs, filepath.Join(contextDir, title), data, 0o644); e != nil {
			return fmt.Errorf("writing context %s: %w", title, e)
		}
	}

	for _, layer := range afstore.Layers(manifest, spec.OctetStream) {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, err := afstore.FetchLayer(ctx, w.store, layer)
		if err != nil {
			return fmt.Errorf("fetching file %s: %w", title, err)
		}

		dst := filepath.Base(title)
		if e := util.WriteFile(fs, filepath.Join(dataDir, dst), data, 0o644); e != nil {
			return fmt.Errorf("writing file %s: %w", dst, e)
		}
	}

	return nil
}

func (w *workspace) installRuntime(ctx context.Context, fs billy.Filesystem, af *spec.Agentfile) error {
	if w.localRuntime != "" {
		return copyLocalFile(fs, w.hostFS, w.localRuntime, RuntimeBin)
	}

	if af.Agent.Runtime != "" {
		return pullBin(ctx, w.ociPuller, fs, spec.ParseReference(af.Agent.Runtime), RuntimeBin)
	}

	return nil
}

func (w *workspace) installTools(ctx context.Context, fs billy.Filesystem, af *spec.Agentfile) error {
	for _, t := range af.Agent.Bins {
		dest := filepath.Join(binDir, t.Name)
		if err := pullBin(ctx, w.ociPuller, fs, spec.ParseReference(t.Image), dest); err != nil {
			return fmt.Errorf("pulling tool %s: %w", t.Name, err)
		}
	}

	return nil
}

func pullBin(ctx context.Context, puller agentoci.Puller, fs billy.Filesystem, ref spec.Reference, dest string) error {
	f, err := fs.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	pullErr := puller(ctx, ref, f)

	if closeErr := f.Close(); pullErr == nil {
		pullErr = closeErr
	}

	return pullErr
}

// copyLocalFile copies src (read through hostFS) to dest (written into
// fs, the agent's chroot). hostFS is typically osfs.New("/") in
// production and memfs in tests.
func copyLocalFile(fs, hostFS billy.Filesystem, src, dest string) error {
	in, err := hostFS.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := fs.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	_, cpErr := io.Copy(out, in)

	if closeErr := out.Close(); cpErr == nil {
		cpErr = closeErr
	}

	return cpErr
}
