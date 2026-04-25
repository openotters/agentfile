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
