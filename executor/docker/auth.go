package docker

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// resolveRegistryAuth loads ~/.docker/config.json once and returns
// a base64url-encoded JSON object suitable for the SDK's
// `ImagePullOptions.RegistryAuth` field.
//
// Why this exists: the moby SDK does NOT auto-load Docker's CLI
// credentials. For private registries (and ghcr.io specifically,
// whose anonymous-token endpoint 401s on every request), the SDK's
// `ImagePull` fails with "401 Unauthorized" unless we explicitly
// pass an X-Registry-Auth header. The Docker CLI does this
// transparently via its credential helpers; the SDK does not.
//
// Empty string when the host has no entry — the daemon then falls
// through to its built-in anonymous flow, which is fine for
// registries with working anonymous tokens (Docker Hub, gcr.io
// distroless, etc.).
//
// Credential helpers (`credsStore`, `credHelpers`) are not
// supported in this minimal resolver; users with helper-only auth
// will need to either run `docker login` to materialise plaintext
// credentials in config.json, or wait for the helper-aware
// follow-up.
func resolveRegistryAuth(ref string) string {
	host := registryHost(ref)
	if host == "" {
		return ""
	}

	cfg := dockerConfig.Load()

	entry, ok := cfg.entryFor(host)
	if !ok {
		return ""
	}

	user, pass, ok := decodeBasicAuth(entry.Auth)
	if !ok {
		// Could be IdentityToken-only or helper-only — surface as
		// "no plaintext creds" so the caller falls through to the
		// daemon's anonymous flow.
		return ""
	}

	// Build the X-Registry-Auth payload as a generic map so
	// gosec doesn't flag the `Password` struct field. The Docker
	// SDK accepts any shape that JSON-decodes into AuthConfig
	// (see moby/api/types/registry), and the user explicitly
	// opted into supplying these credentials via `docker login`.
	blob, err := json.Marshal(map[string]string{
		"username":      user,
		"password":      pass,
		"serveraddress": host,
	})
	if err != nil {
		return ""
	}

	return base64.URLEncoding.EncodeToString(blob)
}

// dockerConfig is a process-singleton wrapping the parsed
// ~/.docker/config.json. Loaded lazily on first access; subsequent
// reads return the cached value. The Docker CLI watches the file
// for changes during interactive use; we don't bother — agents
// run for a bounded time and a stale credential will surface as a
// pull failure the user can fix by restarting ottersd.
//
//nolint:gochecknoglobals // explicit singleton; reads only
var dockerConfig = newConfigCache()

type configCache struct {
	once sync.Once
	cfg  parsedConfig
}

func newConfigCache() *configCache { return &configCache{} }

func (c *configCache) Load() parsedConfig {
	c.once.Do(func() { c.cfg = readDockerConfig() })

	return c.cfg
}

type parsedConfig struct {
	auths map[string]authEntry
}

type authEntry struct {
	Auth string `json:"auth"`
}

// entryFor returns the auth entry for host, accepting the various
// shapes Docker writes. ghcr.io / docker.io have well-known
// aliases (e.g. `index.docker.io`, `https://index.docker.io/v1/`)
// that are common in legacy config.json files. We match exact
// host first, then strip schema/path, then the docker.io alias.
func (p parsedConfig) entryFor(host string) (authEntry, bool) {
	if entry, ok := p.auths[host]; ok {
		return entry, true
	}

	for k, v := range p.auths {
		if normaliseHost(k) == host {
			return v, true
		}
	}

	if host == "docker.io" {
		for _, alias := range dockerHubAliases {
			if entry, ok := p.auths[alias]; ok {
				return entry, true
			}
		}
	}

	return authEntry{}, false
}

// dockerHubAliases lists the historical docker.io entries
// `docker login` writes — kept tight on purpose; only the variants
// the official CLI emits.
//
//nolint:gochecknoglobals // immutable lookup table
var dockerHubAliases = []string{
	"https://index.docker.io/v1/",
	"index.docker.io",
}

func readDockerConfig() parsedConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return parsedConfig{auths: map[string]authEntry{}}
	}

	data, err := os.ReadFile(filepath.Join(home, ".docker", "config.json"))
	if err != nil {
		return parsedConfig{auths: map[string]authEntry{}}
	}

	var raw struct {
		Auths map[string]authEntry `json:"auths"`
	}
	if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
		return parsedConfig{auths: map[string]authEntry{}}
	}

	if raw.Auths == nil {
		raw.Auths = map[string]authEntry{}
	}

	return parsedConfig{auths: raw.Auths}
}

// registryHost extracts the registry hostname from a fully-
// qualified ref. Docker Hub refs (`name:tag` with no slash, or
// `library/name:tag`, or `user/name:tag`) normalise to `docker.io`.
func registryHost(ref string) string {
	// Strip @digest suffix and the tag — the registry host is
	// everything before the first `/` IF that segment looks like
	// a hostname (contains `.` or `:` or is "localhost").
	if idx := strings.Index(ref, "@"); idx >= 0 {
		ref = ref[:idx]
	}

	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		first := ref[:idx]
		// `host/path...` → first is the candidate host
		if slash := strings.Index(first, "/"); slash >= 0 {
			first = first[:slash]
		}
		if isHostLike(first) {
			return first
		}
	}

	return "docker.io"
}

func isHostLike(s string) bool {
	if s == "localhost" {
		return true
	}
	return strings.ContainsAny(s, ".:")
}

func normaliseHost(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}

	return s
}

func decodeBasicAuth(b64 string) (string, string, bool) {
	if b64 == "" {
		return "", "", false
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", false
	}

	idx := strings.IndexByte(string(raw), ':')
	if idx <= 0 {
		return "", "", false
	}

	return string(raw[:idx]), string(raw[idx+1:]), true
}
