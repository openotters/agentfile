package spec_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openotters/agentfile/spec"
)

func TestParseFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "Agentfile")

	body := "FROM scratch\nNAME parsefile-test\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	af, err := spec.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if af.Agent == nil || af.Agent.Name != "parsefile-test" {
		t.Fatalf("unexpected result: %+v", af)
	}
}

func TestParseFile_NotFound(t *testing.T) {
	t.Parallel()

	if _, err := spec.ParseFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ParseFile(missing) = nil, want error")
	}
}
