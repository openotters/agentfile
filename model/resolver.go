// Package model resolves LLM credentials at runtime. A Resolver maps a
// model string (e.g. "anthropic/claude-haiku-4-5") to the API base URL
// and API key that the runtime should use.
package model

// Resolver returns the API base URL and key for a given model.
// Implementations may read from env, secret stores, or static config.
type Resolver func(model string) (apiURL string, apikey string, err error)

// StaticResolver returns a Resolver that always returns the same apiURL
// and apiKey regardless of the model passed in. Useful for tests and
// single-provider deployments.
func StaticResolver(apiURL, apiKey string) Resolver {
	return func(_ string) (string, string, error) {
		return apiURL, apiKey, nil
	}
}
