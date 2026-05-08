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

	"github.com/openotters/agentfile/executor"
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
		// home/ + standard XDG subdirs are pre-created so tool
		// subprocesses that touch $HOME / $XDG_* (wget's HSTS file,
		// curl's netrc, …) have writable destinations inside the
		// agent root and don't pollute the operator's real home.
		"home",
		filepath.Join("home", ".config"),
		filepath.Join("home", ".cache"),
		filepath.Join("home", ".local", "share"),
	}
)

// ErrPull indicates the agent image could not be loaded from the OCI store.
var ErrPull = fmt.Errorf("agent pull error")

// ErrModel indicates the model resolver could not provide credentials for the
// agentfile's declared model — typically a missing provider entry in
// ~/.otters/providers.yaml or a model not in the provider's allowlist.
var ErrModel = fmt.Errorf("model resolve error")

// workspace holds everything needed to materialize an agent workspace.
type workspace struct {
	store          oras.ReadOnlyTarget
	ref            spec.Reference
	overrides      []spec.Override
	ociPuller      agentoci.Puller
	modelResolver  model.Resolver
	digestResolver DigestResolver
	imageRef       string
	localRuntime   string
	mounts         []executor.Mount
	// toolBinaryPath optionally overrides the per-tool binary path
	// stamped into agent.yaml. Returning a non-empty string for a
	// given tool name uses that string verbatim; returning ""
	// keeps the default `usr/bin/<name>` (chroot-relative). Used by
	// the docker executor to point the runtime at the in-container
	// image-mount path (`/opt/bins/<name>/<name>`) instead of the
	// disk path the system executor copies binaries to.
	toolBinaryPath func(name string) string
	// hostFS is a non-chrooted filesystem used for operations on real
	// host paths: the mount symlink target, the local-runtime source
	// file. NewAgent defaults to osfs.New("/"); tests substitute memfs.
	// Distinct from fs (the per-agent chroot) because billy's chroot
	// would rewrite absolute symlink targets and host-file sources.
	hostFS billy.Filesystem
}

// materialize runs the full system-executor pipeline: content +
// runtime/BIN binary install. Used by the system Provider only.
func (w *workspace) materialize(
	ctx context.Context, fs billy.Filesystem, id uuid.UUID, addr string,
) (*executor.Runtime, error) {
	rt, af, err := w.materializeContent(ctx, fs, id, addr)
	if err != nil {
		return nil, err
	}

	if e := w.installRuntime(ctx, fs, af); e != nil {
		return nil, e
	}

	if e := w.installTools(ctx, fs, af); e != nil {
		return nil, e
	}

	return rt, nil
}

// materializeContent does everything materialize does except
// installing the runtime + BIN binaries to disk: pulls the agentfile
// manifest, applies overrides, creates the FHS dirs, extracts CONTEXT
// + ADD layers, resolves model credentials, builds the
// executor.Runtime, writes WORKSPACE.md / AGENT.md / agent.yaml, and
// applies user mount symlinks. Returns the agentfile spec so callers
// (the system Provider's binary-install step) can drive the rest of
// the pipeline without re-loading.
func (w *workspace) materializeContent(
	ctx context.Context, fs billy.Filesystem, id uuid.UUID, addr string,
) (*executor.Runtime, *spec.Agentfile, error) {
	manifest, af, err := afstore.Load(ctx, w.store, w.ref)
	if err != nil {
		return nil, nil, errors.Join(ErrPull, fmt.Errorf("loading %s: %w", w.ref, err))
	}

	af.Apply(w.overrides...)

	for _, dir := range fhsDirs {
		if mkdirErr := fs.MkdirAll(dir, 0o755); mkdirErr != nil {
			return nil, nil, fmt.Errorf("creating %s: %w", dir, mkdirErr)
		}
	}

	if e := w.extractLayers(ctx, fs, manifest); e != nil {
		return nil, nil, e
	}

	agentMD := spec.GenerateAgentMD(af)
	if e := util.WriteFile(fs, filepath.Join(contextDir, "AGENT.md"), []byte(agentMD), 0o644); e != nil {
		return nil, nil, fmt.Errorf("writing AGENT.md: %w", e)
	}

	rt := &executor.Runtime{
		ID:     id,
		Source: af,
		ResolvedConfig: executor.ResolvedConfig{
			Name:  af.Agent.Name,
			Model: af.Agent.Model,
			Addr:  addr,
			Exec:  af.Agent.Exec,
		},
	}

	if w.modelResolver != nil {
		apiURL, apiKey, resolveErr := w.modelResolver(af.Agent.Model)
		if resolveErr != nil {
			return nil, nil, errors.Join(ErrModel, fmt.Errorf("resolving model: %w", resolveErr))
		}

		rt.APIBase = apiURL
		rt.APIKey = apiKey
	}

	for _, t := range af.Agent.Bins {
		binary := filepath.Join(binDir, t.Name)
		if w.toolBinaryPath != nil {
			if override := w.toolBinaryPath(t.Name); override != "" {
				binary = override
			}
		}

		rt.Tools = append(rt.Tools, executor.ResolvedTool{
			Name:        t.Name,
			Description: t.Description,
			Binary:      binary,
			Ref:         t.Image,
			Digest:      w.resolveDigest(t.Image),
		})
	}

	if w.imageRef != "" || af.Agent.Runtime != "" {
		rt.Provenance = &executor.Provenance{
			ImageDigest:   w.resolveDigest(w.imageRef),
			RuntimeRef:    af.Agent.Runtime,
			RuntimeDigest: w.resolveDigest(af.Agent.Runtime),
		}
	}

	if e := rt.WriteTo(fs); e != nil {
		return nil, nil, fmt.Errorf("writing runtime config: %w", e)
	}

	if e := w.writeWorkspaceContext(fs); e != nil {
		return nil, nil, e
	}

	if e := w.applyMounts(fs); e != nil {
		return nil, nil, e
	}

	return rt, af, nil
}

// workspaceContextMarkdown renders the body of WORKSPACE.md given the
// real on-disk root of the agent's workspace. Kept pure so the exact
// phrasing the LLM sees can be regression-tested without exercising
// billy. An empty root omits the "Your workspace on the host" line and
// leaves layout paths un-prefixed (test-only convenience).
//
// All layout paths are emitted as **full absolute host paths** rooted
// at `root`. There is no chroot, so the agent must use real host
// paths in tool calls — e.g. `cat /Users/me/.otters/agents/<id>/etc/context/WORKSPACE.md`,
// not `cat /etc/context/WORKSPACE.md`.
func workspaceContextMarkdown(root string) []byte {
	var b bytes.Buffer

	paths := workspacePaths(root)

	b.WriteString("# Your workspace\n\n")
	b.WriteString("You run as a subprocess on the user's host machine. ")
	b.WriteString("The openotters daemon prepares a dedicated directory ")
	b.WriteString("for you with a small FHS-style layout. Your environment ")
	b.WriteString("is locked down (PATH points only at your own bin dir, ")
	b.WriteString("HOME / TMPDIR / XDG_* live inside this directory) so ")
	b.WriteString("tools that touch `~/.cache` or `~/.config` write inside ")
	b.WriteString("your tree, not the user's real home.\n\n")

	if root != "" {
		fmt.Fprintf(&b, "Your workspace on the host: `%s`\n\n", root)
		b.WriteString("**There is no chroot.** Every path below is a real, absolute path on the host. ")
		b.WriteString("Use these paths verbatim in tool calls — `/etc/context/...` and similar short ")
		b.WriteString("forms do not exist outside this directory tree.\n\n")
	}

	writeLayout(&b, paths)
	writeRules(&b, root, paths)

	return b.Bytes()
}

// workspacePathSet groups the absolute layout paths so the renderer
// can pass them around as a single value.
type workspacePathSet struct {
	context, data, bin, runtime, workspace, home, tmp, varlib, mounts string
}

func workspacePaths(root string) workspacePathSet {
	context := filepath.Join(root, "etc", "context")
	return workspacePathSet{
		context:   context,
		data:      filepath.Join(root, "etc", "data"),
		bin:       filepath.Join(root, "usr", "bin"),
		runtime:   filepath.Join(root, "usr", "local", "bin", "runtime"),
		workspace: filepath.Join(root, "workspace"),
		home:      filepath.Join(root, "home"),
		tmp:       filepath.Join(root, "tmp"),
		varlib:    filepath.Join(root, "var", "lib"),
		mounts:    filepath.Join(context, "MOUNTS.md"),
	}
}

func writeLayout(b *bytes.Buffer, p workspacePathSet) {
	b.WriteString("Layout (full absolute paths):\n\n")
	fmt.Fprintf(b, "- `%s/` — your system-prompt context files "+
		"(this file lives here, alongside AGENT.md and any MOUNTS.md).\n", p.context)
	fmt.Fprintf(b, "- `%s/` — static files baked in via `ADD` directives.\n", p.data)
	fmt.Fprintf(b, "- `%s/` — your BIN tools "+
		"(each BIN directive installs one binary here; the ONLY entry on $PATH).\n", p.bin)
	fmt.Fprintf(b, "- `%s` — the runtime you yourself are.\n", p.runtime)
	fmt.Fprintf(b, "- `%s/` — your default CWD; scratch space for session artefacts.\n", p.workspace)
	fmt.Fprintf(b, "- `%s/` — `$HOME` for tool subprocesses "+
		"(`~/.config`, `~/.cache`, `~/.local/share` live here).\n", p.home)
	fmt.Fprintf(b, "- `%s/` — `$TMPDIR`; short-lived files.\n", p.tmp)
	fmt.Fprintf(b, "- `%s/` — persistent state (the memory DB lives here).\n\n", p.varlib)
}

func writeRules(b *bytes.Buffer, root string, p workspacePathSet) {
	b.WriteString("Working-directory rules:\n\n")
	fmt.Fprintf(b, "- Default CWD is `%s/`; relative paths in tool args resolve there.\n", p.workspace)
	fmt.Fprintf(b, "- Prefer writing inside `%s/`, `%s/`, or `%s/`; treat `%s/etc/` and `%s/usr/` as read-only.\n",
		p.workspace, p.tmp, p.home, root, root)
	fmt.Fprintf(b, "- If `%s` exists, those paths are user-mounted host directories — "+
		"the only \"outside\" paths you should explore or modify.\n", p.mounts)
	b.WriteString("- Without a MOUNTS.md entry, do not probe arbitrary host paths " +
		"(`/Users/…`, `/home/…`, `/etc/passwd`) — there is no sandbox enforcing this " +
		"today, treat the boundary as a discipline rule.\n")
}

// writeWorkspaceContext drops /etc/context/WORKSPACE.md describing
// the agent's filesystem layout and real host path. Useful because
// the LLM otherwise has no idea what "its" filesystem looks like
// when a tool like `ls` or `pwd` is invoked — and without this
// note it'll happily `ls /` and see the host root.
//
// resolveDigest is the safe-call shim around the optional
// DigestResolver: nil resolver and empty refs both return "" without
// panicking, so the caller can ignore best-effort lookups.
func (w *workspace) resolveDigest(ref string) string {
	if w.digestResolver == nil || ref == "" {
		return ""
	}

	return w.digestResolver(ref)
}

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
func mountsContextMarkdown(mounts []executor.Mount) []byte {
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
