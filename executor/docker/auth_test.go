//nolint:testpackage // tests unexported helpers
package docker

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// White-box (package docker) so we can hit registryHost,
// isHostLike, normaliseHost, decodeBasicAuth — all pure string
// manipulation that doesn't need a Docker daemon.

func TestRegistryHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref  string
		want string
	}{
		{"ghcr.io/openotters/runtime:latest", "ghcr.io"},
		{"ghcr.io/openotters/runtime", "ghcr.io"},
		{"localhost:5000/foo:bar", "localhost:5000"},
		{"127.0.0.1:5051/openotters/runtime:latest", "127.0.0.1:5051"},
		{"library/alpine:latest", "docker.io"},
		{"alpine:latest", "docker.io"},
		{"alpine", "docker.io"},
		{"ghcr.io/openotters/runtime@sha256:abc", "ghcr.io"},
	}

	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			t.Parallel()
			if got := registryHost(c.ref); got != c.want {
				t.Errorf("registryHost(%q) = %q, want %q", c.ref, got, c.want)
			}
		})
	}
}

func TestIsHostLike(t *testing.T) {
	t.Parallel()

	cases := []struct {
		s    string
		want bool
	}{
		{"localhost", true},
		{"ghcr.io", true},
		{"127.0.0.1:5000", true},
		{"library", false},
		{"openotters", false},
		{"", false},
	}

	for _, c := range cases {
		t.Run(c.s, func(t *testing.T) {
			t.Parallel()
			if got := isHostLike(c.s); got != c.want {
				t.Errorf("isHostLike(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestNormaliseHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"https://index.docker.io/v1/", "index.docker.io"},
		{"http://ghcr.io", "ghcr.io"},
		{"ghcr.io", "ghcr.io"},
		{"ghcr.io/path/extra", "ghcr.io"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			if got := normaliseHost(c.in); got != c.want {
				t.Errorf("normaliseHost(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDecodeBasicAuth(t *testing.T) {
	t.Parallel()

	good := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		u, p, ok := decodeBasicAuth(good)
		if !ok || u != "alice" || p != "s3cret" {
			t.Errorf("decodeBasicAuth(%q) = (%q,%q,%v); want (alice,s3cret,true)", good, u, p, ok)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := decodeBasicAuth(""); ok {
			t.Error("decodeBasicAuth empty should fail")
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := decodeBasicAuth("!!!notbase64!!!"); ok {
			t.Error("decodeBasicAuth bad-base64 should fail")
		}
	})

	t.Run("missing colon", func(t *testing.T) {
		t.Parallel()
		nocolon := base64.StdEncoding.EncodeToString([]byte("nocolon"))
		if _, _, ok := decodeBasicAuth(nocolon); ok {
			t.Error("decodeBasicAuth no-colon should fail")
		}
	})

	t.Run("colon at start", func(t *testing.T) {
		t.Parallel()
		colonfirst := base64.StdEncoding.EncodeToString([]byte(":secret"))
		if _, _, ok := decodeBasicAuth(colonfirst); ok {
			t.Error("decodeBasicAuth empty-username should fail")
		}
	})
}

func TestParsedConfig_EntryFor(t *testing.T) {
	t.Parallel()

	pc := parsedConfig{auths: map[string]authEntry{
		"ghcr.io":                      {Auth: "ghcrauth"},
		"https://index.docker.io/v1/":  {Auth: "dockerhubauth"},
		"https://other.example/path/x": {Auth: "otherauth"},
	}}

	t.Run("exact host", func(t *testing.T) {
		t.Parallel()
		e, ok := pc.entryFor("ghcr.io")
		if !ok || e.Auth != "ghcrauth" {
			t.Errorf("ghcr.io entry = (%+v, %v)", e, ok)
		}
	})

	t.Run("normalised host", func(t *testing.T) {
		t.Parallel()
		e, ok := pc.entryFor("other.example")
		if !ok || e.Auth != "otherauth" {
			t.Errorf("normalised entry = (%+v, %v)", e, ok)
		}
	})

	t.Run("docker.io alias", func(t *testing.T) {
		t.Parallel()
		e, ok := pc.entryFor("docker.io")
		if !ok || e.Auth != "dockerhubauth" {
			t.Errorf("docker.io alias entry = (%+v, %v)", e, ok)
		}
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		if _, ok := pc.entryFor("missing.example"); ok {
			t.Error("expected miss")
		}
	})
}

func TestResolveRegistryAuth_NoCredsForHost(t *testing.T) {
	t.Parallel()

	// Hosts unlikely to be in any developer's docker config —
	// the function returns empty when nothing matches, which is
	// the contract that lets ImagePull fall back to the
	// daemon's anonymous flow.
	got := resolveRegistryAuth("nope.invalid.example/some/repo:latest")
	if got != "" {
		t.Errorf("expected empty auth for unknown host, got %q", got)
	}
}

// Covers the JSON shape resolveRegistryAuth produces — even
// without a real docker config we can assert the output is base64-
// encoded JSON with the username/password/serveraddress trio
// expected by the Docker SDK's X-Registry-Auth header. We feed
// values through a parallel implementation since the file-loading
// path needs an actual ~/.docker/config.json.
func TestAuthBlobShape(t *testing.T) {
	t.Parallel()

	blob := authBlobJSON("alice", "s3cret", "ghcr.io")

	decoded, err := base64.URLEncoding.DecodeString(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var got map[string]string
	if jsonErr := json.Unmarshal(decoded, &got); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	want := map[string]string{
		"username":      "alice",
		"password":      "s3cret",
		"serveraddress": "ghcr.io",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("[%s] = %q, want %q", k, got[k], v)
		}
	}
}
