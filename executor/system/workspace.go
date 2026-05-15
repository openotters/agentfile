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
		// opt/bins/ holds per-BIN symlinks for the docker executor —
		// each is a string-target symlink pointing into the
		// container-only /opt/bin-images mount. Created here so the
		// bind-mount destination exists; the symlinks themselves get
		// stamped by stampBinSymlinks (called only by the docker
		// executor's materialise path).
		filepath.Join("opt", "bins"),
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
	usageFetcher   agentoci.UsageFetcher
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
	// capabilities is the daemon-supplied list of daemon-callback
	// tool names (job_submit, …) advertised in agent.yaml. Set via
	// WithCapabilities; left empty when the agent isn't wired to a
	// daemon callback channel.
	capabilities []executor.Capability
	// symlinkBinAt, when non-nil, returns the symlink target string
	// for each BIN tool. Materialise stamps `opt/bins/<name>` →
	// target on the host agent root; the bind-mount surfaces those
	// symlinks at `/opt/bins/` inside the container. Docker uses
	// this so PATH stays flat (`/opt/bins/<name>` IS the executable)
	// while image mounts live hidden under /opt/bin-images.
	symlinkBinAt func(name string) (target string, ok bool)
	// view is the agent-visible filesystem layout used to render
	// WORKSPACE.md. Zero-value means "system defaults derived from
	// fs.Root()". The docker executor sets a container-rooted view
	// (Root="/workspace", BinDirs per-BIN, RuntimeBin in /opt,
	// Isolated=true) so the LLM never sees the host path of its
	// bind mount.
	view executor.WorkspaceView
	// viewBinDirsForTools fills view.BinDirs from the resolved
	// tool list before WORKSPACE.md is written. Docker uses it so
	// each declared BIN gets its in-container image-mount dir
	// without re-parsing the spec.
	viewBinDirsForTools func(toolNames []string) []string
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

	agentMD := spec.GenerateAgentMD(af, w.capabilities)
	if e := util.WriteFile(fs, filepath.Join(contextDir, "AGENT.md"), []byte(agentMD), 0o644); e != nil {
		return nil, nil, fmt.Errorf("writing AGENT.md: %w", e)
	}

	rt, err := w.buildRuntime(af, id, addr)
	if err != nil {
		return nil, nil, err
	}

	if e := rt.WriteTo(fs); e != nil {
		return nil, nil, fmt.Errorf("writing runtime config: %w", e)
	}

	// Populate the view's BIN dirs from the resolved tools when the
	// caller (docker) supplied a builder. System leaves this nil
	// and falls back to the default `<root>/usr/bin` single entry.
	if w.viewBinDirsForTools != nil && len(w.view.BinDirs) == 0 {
		names := make([]string, 0, len(rt.Tools))
		for _, t := range rt.Tools {
			names = append(names, t.Name)
		}

		w.view.BinDirs = w.viewBinDirsForTools(names)
	}

	if e := w.writeWorkspaceContext(fs); e != nil {
		return nil, nil, e
	}

	if e := w.applyMounts(fs); e != nil {
		return nil, nil, e
	}

	if e := w.stampBinSymlinks(fs, af); e != nil {
		return nil, nil, e
	}

	if e := w.installToolDocs(ctx, fs, af); e != nil {
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
// Backwards-compatible shim: callers that have a system-style host
// path call this; the docker executor builds an executor.WorkspaceView
// and routes through workspaceContextMarkdownView instead.
func workspaceContextMarkdown(root string) []byte {
	return workspaceContextMarkdownView(executor.WorkspaceView{Root: root})
}

// workspaceContextMarkdownView renders WORKSPACE.md from a
// WorkspaceView, choosing host-rooted vs. container-rooted phrasing
// based on view.Isolated. A zero-value view (empty Root, etc.) is
// treated as "system defaults derived from the empty root" so tests
// can exercise the no-host-line branch.
func workspaceContextMarkdownView(view executor.WorkspaceView) []byte {
	var b bytes.Buffer

	paths := workspacePaths(view)

	b.WriteString("# Your workspace\n\n")

	if view.Isolated {
		b.WriteString("You run inside an isolated container. ")
		b.WriteString("The openotters daemon bind-mounts a small FHS-style tree at the path below ")
		b.WriteString("and image-mounts the runtime + BIN tools read-only beside it. Your environment ")
		b.WriteString("is locked down (PATH points only at your BIN tool dirs, HOME / TMPDIR / XDG_* ")
		b.WriteString("live inside your tree) so tools that touch `~/.cache` or `~/.config` write ")
		b.WriteString("inside your container, not the user's real home.\n\n")
	} else {
		b.WriteString("You run as a subprocess on the user's host machine. ")
		b.WriteString("The openotters daemon prepares a dedicated directory ")
		b.WriteString("for you with a small FHS-style layout. Your environment ")
		b.WriteString("is locked down (PATH points only at your own bin dir, ")
		b.WriteString("HOME / TMPDIR / XDG_* live inside this directory) so ")
		b.WriteString("tools that touch `~/.cache` or `~/.config` write inside ")
		b.WriteString("your tree, not the user's real home.\n\n")
	}

	if view.Root != "" {
		switch {
		case view.Isolated:
			if view.Root == "/" {
				b.WriteString("Your filesystem is a standard Linux FHS rooted at `/`. ")
				b.WriteString("Each top-level subtree is bind-mounted from the host directly onto the ")
				b.WriteString("matching FHS path — `/etc/context`, `/home`, `/tmp`, `/var/lib`, ")
				b.WriteString("`/workspace`. The host directory is invisible from inside; writes you ")
				b.WriteString("make under these paths show up on the host automatically.\n\n")
			} else {
				fmt.Fprintf(&b, "Your workspace inside the container: `%s`\n\n", view.Root)
				b.WriteString("**Every path below is the path you see from inside the container, not the host.** ")
				b.WriteString("Use these paths verbatim in tool calls; the host directory is bind-mounted ")
				b.WriteString("here so writes inside this tree show up on the host automatically.\n\n")
			}
		default:
			fmt.Fprintf(&b, "Your workspace on the host: `%s`\n\n", view.Root)
			b.WriteString("**There is no chroot.** Every path below is a real, absolute path on the host. ")
			b.WriteString("Use these paths verbatim in tool calls — `/etc/context/...` and similar short ")
			b.WriteString("forms do not exist outside this directory tree.\n\n")
		}
	}

	writeLayout(&b, paths)
	writeRules(&b, view.Root, paths, view.Isolated)

	return b.Bytes()
}

// workspacePathSet groups the absolute layout paths so the renderer
// can pass them around as a single value.
type workspacePathSet struct {
	context, data, workspace, home, tmp, varlib, mounts string
	bins                                                []string
	runtime                                             string
}

func workspacePaths(view executor.WorkspaceView) workspacePathSet {
	root := view.Root
	context := filepath.Join(root, "etc", "context")

	bins := view.BinDirs
	if len(bins) == 0 {
		bins = []string{filepath.Join(root, "usr", "bin")}
	}

	runtime := view.RuntimeBin
	if runtime == "" {
		runtime = filepath.Join(root, "usr", "local", "bin", "runtime")
	}

	workspace := view.WorkspaceDir
	if workspace == "" {
		workspace = filepath.Join(root, "workspace")
	}

	return workspacePathSet{
		context:   context,
		data:      filepath.Join(root, "etc", "data"),
		bins:      bins,
		runtime:   runtime,
		workspace: workspace,
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

	if len(p.bins) == 1 {
		fmt.Fprintf(b, "- `%s/` — your BIN tools "+
			"(each BIN directive installs one binary here; the ONLY entry on $PATH).\n", p.bins[0])
	} else {
		b.WriteString("- BIN tools — one directory per BIN on $PATH " +
			"(each declared BIN is image-mounted at its own path):\n")

		for _, dir := range p.bins {
			fmt.Fprintf(b, "    - `%s/`\n", dir)
		}
	}

	fmt.Fprintf(b, "- `%s` — the runtime you yourself are.\n", p.runtime)
	fmt.Fprintf(b, "- `%s/` — your default CWD; scratch space for session artefacts.\n", p.workspace)
	fmt.Fprintf(b, "- `%s/` — `$HOME` for tool subprocesses "+
		"(`~/.config`, `~/.cache`, `~/.local/share` live here).\n", p.home)
	fmt.Fprintf(b, "- `%s/` — `$TMPDIR`; short-lived files.\n", p.tmp)
	fmt.Fprintf(b, "- `%s/` — persistent state (the memory DB lives here).\n\n", p.varlib)
}

func writeRules(b *bytes.Buffer, root string, p workspacePathSet, isolated bool) {
	b.WriteString("Working-directory rules:\n\n")
	fmt.Fprintf(b, "- Default CWD is `%s/`; relative paths in tool args resolve there.\n", p.workspace)
	fmt.Fprintf(b, "- Prefer writing inside `%s/`, `%s/`, or `%s/`; treat `%s/etc/` and `%s/usr/` as read-only.\n",
		p.workspace, p.tmp, p.home, root, root)
	fmt.Fprintf(b, "- If `%s` exists, those paths are user-mounted host directories — "+
		"the only \"outside\" paths you should explore or modify.\n", p.mounts)

	if isolated {
		b.WriteString("- The container isolates you from the host filesystem; paths like " +
			"`/Users/…`, `/home/…`, `/etc/passwd` are not visible inside, do not probe them.\n")
	} else {
		b.WriteString("- Without a MOUNTS.md entry, do not probe arbitrary host paths " +
			"(`/Users/…`, `/home/…`, `/etc/passwd`) — there is no sandbox enforcing this " +
			"today, treat the boundary as a discipline rule.\n")
	}
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

// runtimeBinary returns the in-container path of the runtime
// executable. Docker sets View.RuntimeBin (typically
// /opt/runtime/runtime, where the image-mount lands). System
// materialises the runtime under the agent root and defaults to
// usr/local/bin/runtime.
func (w *workspace) runtimeBinary() string {
	if w.view.RuntimeBin != "" {
		return w.view.RuntimeBin
	}
	return "/" + RuntimeBin
}

func (w *workspace) writeWorkspaceContext(fs billy.Filesystem) error {
	view := w.view
	if view.Root == "" {
		view.Root = fs.Root()
	}

	return util.WriteFile(fs, filepath.Join(contextDir, "WORKSPACE.md"),
		workspaceContextMarkdownView(view), 0o644)
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

// stampBinSymlinks creates one symlink per declared BIN at
// <agent-root>/opt/bins/<name>. The target string is whatever the
// caller's symlinkBinAt hook returns — typically a container-local
// path like /opt/bin-images/<name>/<name> that resolves at access
// time inside the agent's container. No-op when the caller doesn't
// supply the hook (the system executor installs BIN binaries as
// regular files under usr/bin and doesn't need the indirection).
func (w *workspace) stampBinSymlinks(fs billy.Filesystem, af *spec.Agentfile) error {
	if w.symlinkBinAt == nil || af.Agent == nil {
		return nil
	}

	root := fs.Root()
	if root == "" {
		return fmt.Errorf("bin symlinks require a filesystem with a real root directory")
	}

	binsDir := filepath.Join(root, "opt", "bins")
	if err := w.hostFS.MkdirAll(binsDir, 0o755); err != nil {
		return fmt.Errorf("opt/bins: %w", err)
	}

	for _, t := range af.Agent.Bins {
		target, ok := w.symlinkBinAt(t.Name)
		if !ok || target == "" {
			continue
		}

		link := filepath.Join(binsDir, t.Name)
		_ = w.hostFS.Remove(link)

		if err := w.hostFS.Symlink(target, link); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", link, target, err)
		}
	}

	return nil
}

// mountsContextMarkdown renders the body of MOUNTS.md for a given set
// of mount specs. Pure; exists as its own function so the wording the
// LLM sees is regression-testable.
func mountsContextMarkdown(mounts []executor.Mount) []byte {
	var b bytes.Buffer

	b.WriteString("# Host-mounted paths\n\n")
	b.WriteString("The following paths inside your workspace are backed by directories on the user's host machine. ")
	b.WriteString("Changes through writable mounts are visible to the host immediately. ")
	b.WriteString("Read-only mounts (`(ro)`) reject writes — don't try, the operation will fail. ")
	b.WriteString("Do not assume any other top-level paths are mounted.\n\n")

	for _, m := range mounts {
		fmt.Fprintf(&b, "- `%s`", m.Target)

		if m.ReadOnly {
			b.WriteString(" *(ro)*")
		}

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
	for _, layer := range afstore.Layers(manifest, spec.AgentfileMediaType) {
		data, err := afstore.FetchLayer(ctx, w.store, layer)
		if err != nil {
			return fmt.Errorf("fetching agentfile: %w", err)
		}

		if e := util.WriteFile(fs, filepath.Join("etc", "Agentfile"), data, 0o644); e != nil {
			return fmt.Errorf("writing agentfile: %w", e)
		}
	}

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

// installToolDocs fetches the USAGE.md body declared by each BIN
// manifest and writes it into etc/data/bins/<name>/USAGE.md so the
// runtime's tool loader can append it to the model-facing tool
// description. A nil usageFetcher (test / offline mode) or a BIN
// without the annotation both result in no file written — the
// runtime degrades gracefully because materializeContent stamped
// the path optimistically and the loader treats missing files as
// "no doc". Errors fetching one BIN's doc don't fail the
// materialisation pipeline; we record nothing and move on.
func (w *workspace) installToolDocs(
	ctx context.Context, fs billy.Filesystem, af *spec.Agentfile,
) error {
	if w.usageFetcher == nil {
		return nil
	}

	for _, t := range af.Agent.Bins {
		body, err := w.usageFetcher(ctx, spec.ParseReference(t.Image))
		if err != nil || body == "" {
			continue
		}

		dst := filepath.Join(dataDir, "bins", t.Name, "USAGE.md")
		if mkErr := fs.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
			return fmt.Errorf("creating usage dir for %s: %w", t.Name, mkErr)
		}

		if writeErr := util.WriteFile(fs, dst, []byte(body), 0o644); writeErr != nil {
			return fmt.Errorf("writing usage for %s: %w", t.Name, writeErr)
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

// buildRuntime assembles the in-memory *executor.Runtime from the
// resolved Agentfile. Pulled out of materializeContent to keep that
// function focused on filesystem materialisation steps; this one is
// pure struct-construction (config-only, no I/O on fs).
func (w *workspace) buildRuntime(af *spec.Agentfile, id uuid.UUID, addr string) (*executor.Runtime, error) {
	rt := &executor.Runtime{
		Source: af,
		ResolvedConfig: executor.ResolvedConfig{
			ID:           id,
			Name:         af.Agent.Name,
			Model:        af.Agent.Model,
			Workspace:    "/workspace",
			Configs:      configsFromSpec(af.Agent.Configs),
			Capabilities: w.capabilities,
			Envs:         af.Agent.Envs,
			Mounts:       af.Agent.RuntimeMounts,
			Context:      contextEntries(af.Agent.Contexts, len(w.mounts) > 0 || len(af.Agent.RuntimeMounts) > 0),
			Addr:         addr,
			Exec:         af.Agent.Exec,
		},
	}

	if w.imageRef != "" {
		rt.Image = &executor.OCIRef{
			Ref:    w.imageRef,
			Digest: w.resolveDigest(w.imageRef),
		}
	}

	if af.Agent.Runtime != "" {
		rt.Runtime = &executor.RuntimeRef{
			Ref:    af.Agent.Runtime,
			Digest: w.resolveDigest(af.Agent.Runtime),
			Binary: w.runtimeBinary(),
		}
	}

	if w.modelResolver != nil {
		apiURL, apiKey, resolveErr := w.modelResolver(af.Agent.Model)
		if resolveErr != nil {
			return nil, errors.Join(ErrModel, fmt.Errorf("resolving model: %w", resolveErr))
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

		// Usage is stamped optimistically with the conventional path
		// inside the agent tree. The actual file is written by
		// installToolDocs (system path) / InstallToolDocs (docker
		// path); if the BIN image carries no USAGE.md the file never
		// lands and the runtime's loader silently skips the entry.
		rt.Tools = append(rt.Tools, executor.ResolvedTool{
			Name:        t.Name,
			Description: t.Description,
			Binary:      binary,
			Ref:         t.Image,
			Digest:      w.resolveDigest(t.Image),
			Usage:       "/" + filepath.Join(dataDir, "bins", t.Name, "USAGE.md"),
		})
	}

	return rt, nil
}

// configsFromSpec flattens the spec's []*Config (key/value records)
// into the map[string]string shape agent.yaml carries — one
// kebab-case key per CONFIG directive. Each value is rendered with
// fmt.%v so booleans and numbers serialise consistently.
func configsFromSpec(in []*spec.Config) map[string]string {
	out := map[string]string{}
	for _, c := range in {
		if c == nil || c.Key == "" {
			continue
		}
		out[c.Key] = fmt.Sprintf("%v", c.Value)
	}
	return out
}

// contextEntries returns the ordered list of context files the
// runtime loads into the system prompt, each carrying a name,
// file path, and description. Two sources:
//
//   - Daemon-generated files materialise writes next door — AGENT.md
//     (identity card), WORKSPACE.md (filesystem layout), and
//     MOUNTS.md (only when mounts exist). Daemon supplies their
//     descriptions; they go first so the model reads them before
//     the spec-declared content.
//   - Agentfile-declared CONTEXT directives, in declaration order
//     (SOUL, SCENARIOS, …). Description comes from the directive.
//
// The runtime uses this list verbatim — no dir-scan, no
// Agentfile re-parse. The Name handle drives the context_show
// introspection tool: `context_show SOUL` looks up the matching
// entry and reads its File.
func contextEntries(specCtx []*spec.Context, hasMounts bool) []executor.ContextEntry {
	out := []executor.ContextEntry{
		{
			Name:        "AGENT",
			File:        "/" + filepath.Join(contextDir, "AGENT.md"),
			Description: "Auto-generated identity card — declared bins, environment, capabilities.",
		},
		{
			Name:        "WORKSPACE",
			File:        "/" + filepath.Join(contextDir, "WORKSPACE.md"),
			Description: "Filesystem layout — where each path lives and what's writable.",
		},
	}
	if hasMounts {
		out = append(out, executor.ContextEntry{
			Name:        "MOUNTS",
			File:        "/" + filepath.Join(contextDir, "MOUNTS.md"),
			Description: "Operator-supplied bind mounts — host paths surfaced into the agent tree.",
		})
	}
	for _, c := range specCtx {
		if c == nil || c.Name == "" {
			continue
		}
		out = append(out, executor.ContextEntry{
			Name:        c.Name,
			File:        "/" + filepath.Join(contextDir, c.Name+".md"),
			Description: c.Description,
		})
	}
	return out
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
