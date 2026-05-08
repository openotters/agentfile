package system

import "strings"

// envIndex returns the index of the first KEY= entry in env, or -1
// if absent. Test helper.
func envIndex(env []string, key string) int {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}

	return -1
}
