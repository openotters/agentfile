package spec

import (
	"fmt"
	"strings"

	"github.com/opencontainers/go-digest"
)

const DefaultTag = "latest"

// DefaultSyntax is the syntax value assumed when an Agentfile omits the
// `# syntax=` pragma. Parse stamps this onto Agentfile.Syntax.
const DefaultSyntax = "openotters/agentfile:1"

// SupportedSyntaxes lists every `# syntax=` value the parser accepts.
//
//nolint:gochecknoglobals // logically constant; slice literal can't be const
var SupportedSyntaxes = []string{DefaultSyntax}

// Reference identifies an OCI image by name and tag.
// Name can be local ("meteo") or remote ("ghcr.io/openotters/agents/meteo").
// Tag defaults to "latest" when not specified.
type Reference struct {
	Name string
	Tag  string
}

// ParseReference parses a reference string in the form "name" or "name:tag".
// The tag is the part after the last colon that follows the last slash.
// This correctly handles host:port/name:tag references.
func ParseReference(s string) Reference {
	// Find the last slash to separate the host/path from the potential tag.
	lastSlash := strings.LastIndex(s, "/")

	// Look for a colon after the last slash (that's the tag separator).
	tagPart := s
	if lastSlash >= 0 {
		tagPart = s[lastSlash:]
	}

	if idx := strings.LastIndex(tagPart, ":"); idx > 0 {
		// The colon is in tagPart; convert back to full string index.
		fullIdx := idx
		if lastSlash >= 0 {
			fullIdx += lastSlash
		}

		return Reference{Name: s[:fullIdx], Tag: s[fullIdx+1:]}
	}

	return Reference{Name: s, Tag: DefaultTag}
}

// String returns the reference as "name:tag".
func (r Reference) String() string {
	tag := r.Tag
	if tag == "" {
		tag = DefaultTag
	}

	return r.Name + ":" + tag
}

// IsQualified reports whether name carries a registry-host component.
// Heuristic matches containerd / docker reference parsers: the first
// slash-separated segment is a host when it contains "." (a TLD) or
// ":" (a port), or equals "localhost". Bare names like "foo" or
// "agents/foo" are unqualified — a caller with a default registry
// fills the host in via QualifyWithDefault.
//
// Accepts either a bare name ("agents/foo") or a full reference
// string ("agents/foo:v1") — the trailing tag, if any, is stripped
// before the host-detection runs so callers don't need to ParseReference
// first.
func IsQualified(name string) bool {
	// Drop the trailing ":tag" using the same precedence rule as
	// ParseReference: the tag separator is the last colon after the
	// last slash (or anywhere in the string when there's no slash).
	parsed := ParseReference(name)

	first := parsed.Name
	if i := strings.Index(parsed.Name, "/"); i >= 0 {
		first = parsed.Name[:i]
	}

	if first == "localhost" {
		return true
	}

	return strings.ContainsAny(first, ".:")
}

// QualifyWithDefault returns ref with defaultRegistry prepended to its
// Name iff Name isn't already qualified. defaultRegistry should not
// include a scheme or trailing slash; an empty defaultRegistry is a
// no-op (callers without a default fall back to the unmodified ref).
func QualifyWithDefault(ref Reference, defaultRegistry string) Reference {
	if defaultRegistry == "" || IsQualified(ref.Name) {
		return ref
	}

	return Reference{
		Name: defaultRegistry + "/" + ref.Name,
		Tag:  ref.Tag,
	}
}

// Validate checks that the reference has at least a name.
func (r Reference) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("reference name is required")
	}

	return nil
}

// ReferenceWithDigest pairs a Reference with a content-addressed digest
// from the OCI store.
type ReferenceWithDigest struct {
	Reference Reference
	Digest    digest.Digest
}

// String returns "name:tag@digest".
func (r ReferenceWithDigest) String() string {
	return r.Reference.String() + "@" + r.Digest.String()
}
