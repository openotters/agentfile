package spec

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GenerateAgentMD generates markdown documentation from an Agentfile.
func GenerateAgentMD(af *Agentfile) string {
	a := af.Agent
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", a.Name)

	if desc, ok := a.Labels["description"]; ok {
		b.WriteString(desc + "\n\n")
	}

	if len(a.Bins) > 0 {
		b.WriteString("## Binaries\n\n")

		for _, t := range a.Bins {
			fmt.Fprintf(&b, "- **%s**", t.Name)

			if t.Description != "" {
				fmt.Fprintf(&b, " — %s", t.Description)
			}

			b.WriteByte('\n')

			if t.Usage != "" {
				for _, line := range strings.Split(t.Usage, "\n") {
					b.WriteString("  " + line + "\n")
				}
			}
		}

		b.WriteByte('\n')
	}

	if len(a.Adds) > 0 {
		b.WriteString("## Data Files\n\n")
		b.WriteString("| File | Description |\n")
		b.WriteString("|------|-------------|\n")

		for _, add := range a.Adds {
			desc := add.Description
			if desc == "" {
				desc = "-"
			}

			fmt.Fprintf(&b, "| %s | %s |\n", filepath.Base(add.Dst), desc)
		}

		b.WriteByte('\n')
	}

	if len(a.Envs) > 0 {
		b.WriteString("## Environment\n\n")
		b.WriteString("| Key | Value | Description |\n")
		b.WriteString("|-----|-------|-------------|\n")

		for _, env := range a.Envs {
			desc := env.Description
			if desc == "" {
				desc = "-"
			}

			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", env.Key, env.Value, desc)
		}

		b.WriteByte('\n')
	}

	b.WriteString("## Filesystem\n\n")
	b.WriteString("| Path | Access |\n")
	b.WriteString("|------|--------|\n")
	b.WriteString("| workspace/ | read-write |\n")
	b.WriteString("| tmp/ | read-write |\n")
	b.WriteString("| var/lib/ | read-write |\n")

	return b.String()
}
