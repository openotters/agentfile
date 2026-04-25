package spec_test

import (
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/openotters/agentfile/spec"
)

func TestParseReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       string
		wantName string
		wantTag  string
	}{
		{"meteo", "meteo", "latest"},
		{"meteo:v1", "meteo", "v1"},
		{"ghcr.io/openotters/agents/meteo", "ghcr.io/openotters/agents/meteo", "latest"},
		{"ghcr.io/openotters/agents/meteo:1.0.0", "ghcr.io/openotters/agents/meteo", "1.0.0"},
		// host:port must not be read as a tag — disambiguation is the
		// last colon after the last slash.
		{"localhost:5000/meteo", "localhost:5000/meteo", "latest"},
		{"localhost:5000/meteo:dev", "localhost:5000/meteo", "dev"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := spec.ParseReference(tc.in)
			if got.Name != tc.wantName || got.Tag != tc.wantTag {
				t.Fatalf("ParseReference(%q) = {%q, %q}, want {%q, %q}",
					tc.in, got.Name, got.Tag, tc.wantName, tc.wantTag)
			}
		})
	}
}

func TestReferenceString(t *testing.T) {
	t.Parallel()

	if got := (spec.Reference{Name: "meteo", Tag: "v1"}).String(); got != "meteo:v1" {
		t.Fatalf("String() = %q, want %q", got, "meteo:v1")
	}

	// Empty tag falls back to DefaultTag.
	if got := (spec.Reference{Name: "meteo"}).String(); got != "meteo:latest" {
		t.Fatalf("String() empty tag = %q, want %q", got, "meteo:latest")
	}
}

func TestReferenceValidate(t *testing.T) {
	t.Parallel()

	if err := (spec.Reference{Name: "meteo"}).Validate(); err != nil {
		t.Fatalf("Validate(named) = %v, want nil", err)
	}

	err := (spec.Reference{}).Validate()
	if err == nil {
		t.Fatal("Validate(empty) = nil, want error")
	}

	if !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("Validate(empty) err = %v, want 'name is required'", err)
	}
}

func TestReferenceWithDigestString(t *testing.T) {
	t.Parallel()

	r := spec.ReferenceWithDigest{
		Reference: spec.Reference{Name: "meteo", Tag: "v1"},
		Digest:    digest.FromString("hello"),
	}

	got := r.String()
	want := "meteo:v1@" + digest.FromString("hello").String()

	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestSupportedSyntaxes(t *testing.T) {
	t.Parallel()

	// Public constants — guarantee the value the parser accepts hasn't
	// silently shifted under callers that pin `# syntax=`.
	if spec.DefaultSyntax != "openotters/agentfile:1" {
		t.Fatalf("DefaultSyntax = %q", spec.DefaultSyntax)
	}

	if len(spec.SupportedSyntaxes) == 0 || spec.SupportedSyntaxes[0] != spec.DefaultSyntax {
		t.Fatalf("SupportedSyntaxes = %v, want first entry to equal DefaultSyntax", spec.SupportedSyntaxes)
	}
}
