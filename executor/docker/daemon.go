package docker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	mobyclient "github.com/moby/moby/client"
)

// MinEngineVersion is the lowest Docker Engine major release that
// supports `--mount type=image` for non-experimental container
// creation. Docker 28.0 (February 2025) is when the type validator
// stopped rejecting it on the daemon side; engines 25–27 advertise
// the API field but ContainerCreate still errors with "mount type
// unknown". We fail fast at Provider construction with a pointed
// error message rather than letting the failure surface midway
// through Run.
const MinEngineVersion = 28

// ErrEngineTooOld is returned by Verify when the daemon's Engine
// version is below MinEngineVersion.
var ErrEngineTooOld = errors.New("docker engine too old")

// ErrNoSnapshotter is returned by Verify when the daemon doesn't
// expose the containerd snapshotter — required for openotters'
// custom OCI mediatypes.
var ErrNoSnapshotter = errors.New("containerd snapshotter not enabled")

// ErrDaemonUnreachable is returned by Verify when Info() / Version()
// can't reach the daemon at all (socket missing, permission denied,
// etc.). Wraps the underlying SDK error so callers can use
// errors.Is.
var ErrDaemonUnreachable = errors.New("docker daemon unreachable")

// Verify probes the daemon at Provider construction time:
//
//   - daemon is reachable (Info() / ServerVersion() succeed),
//   - Engine version is ≥ MinEngineVersion,
//   - containerd snapshotter is enabled (the daemon's Info.Driver
//     reports a containerd snapshotter rather than the classic
//     graphdriver).
//
// Returns a multi-line, copy-pasteable error message when any check
// fails. Each error wraps a sentinel (ErrDaemonUnreachable,
// ErrEngineTooOld, ErrNoSnapshotter) so callers can switch on
// failure mode.
func Verify(ctx context.Context, cli Client) error {
	verResult, err := cli.ServerVersion(ctx, mobyclient.ServerVersionOptions{})
	if err != nil {
		return fmt.Errorf("%w: %w (start Docker / Colima / Docker Desktop, or set DOCKER_HOST)", ErrDaemonUnreachable, err)
	}

	major, err := parseEngineMajor(verResult.Version)
	if err != nil {
		return fmt.Errorf("docker: parse engine version %q: %w", verResult.Version, err)
	}

	if major < MinEngineVersion {
		return fmt.Errorf(
			"%w: docker engine %s found, openotters docker executor requires ≥ %d.0 for image-mount support. "+
				"On Colima, upgrade to a release shipping Docker 28+ (colima ≥ 0.9 once available, or "+
				"manually update the VM's docker engine). On Docker Desktop, ensure ≥ 4.40. Otherwise "+
				"use the system executor: `ottersd serve --executor system`",
			ErrEngineTooOld, verResult.Version, MinEngineVersion,
		)
	}

	infoResult, err := cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return fmt.Errorf("%w: Info: %w", ErrDaemonUnreachable, err)
	}

	if !hasContainerdSnapshotter(infoResult.Info.DriverStatus) {
		return fmt.Errorf(
			"%w: enable the containerd image store, then restart the docker daemon. "+
				"Docker Desktop ≥4.34 has it on by default. "+
				"Native Linux dockerd: add `{\"features\":{\"containerd-snapshotter\":true}}` "+
				"to /etc/docker/daemon.json and `systemctl restart docker`. "+
				"Colima: `colima ssh -- sudo sh -c \"echo '{\\\"features\\\":"+
				"{\\\"containerd-snapshotter\\\":true}}' > /etc/docker/daemon.json && "+
				"systemctl restart docker\"` (or recreate with `colima delete && "+
				"colima start --runtime docker` after enabling the option in "+
				"~/.colima/default/colima.yaml under `docker:`)",
			ErrNoSnapshotter,
		)
	}

	return nil
}

// parseEngineMajor extracts the major version from a Docker
// version string like "27.4.0" → 27. Tolerates "v"-prefixed strings
// for safety.
func parseEngineMajor(v string) (int, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")

	dot := strings.IndexByte(v, '.')
	if dot <= 0 {
		return 0, fmt.Errorf("no major.minor separator in %q", v)
	}

	return strconv.Atoi(v[:dot])
}

// driverStatusType is the key Docker info uses to surface the
// rootfs driver kind in DriverStatus. The classic graphdriver
// reports keys like "Backing Filesystem" / "Supports d_type"; the
// containerd snapshotter reports `driver-type=io.containerd.snapshotter…`.
const driverStatusType = "driver-type"

// hasContainerdSnapshotter scans Docker Info's DriverStatus for the
// containerd-snapshotter marker.
func hasContainerdSnapshotter(driverStatus [][2]string) bool {
	for _, row := range driverStatus {
		if row[0] == driverStatusType && strings.Contains(row[1], "io.containerd.snapshotter") {
			return true
		}
	}

	return false
}
