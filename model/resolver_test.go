package model_test

import (
	"testing"

	"github.com/openotters/agentfile/model"
)

func TestStaticResolver(t *testing.T) {
	t.Parallel()

	r := model.StaticResolver("https://api.example.com/v1", "secret")

	// Same result for any model string.
	for _, m := range []string{"anthropic/claude-haiku-4-5", "openai/gpt-4", ""} {
		url, key, err := r(m)
		if err != nil {
			t.Fatalf("StaticResolver(%q) err = %v", m, err)
		}

		if url != "https://api.example.com/v1" || key != "secret" {
			t.Fatalf("StaticResolver(%q) = (%q, %q), want (%q, %q)",
				m, url, key, "https://api.example.com/v1", "secret")
		}
	}
}
