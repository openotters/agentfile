package executor

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openotters/agentfile/spec"
)

func GenerateAgentMD(af *spec.Agentfile) string {
	agent := af.Agent
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", agent.Name)

	if desc, ok := agent.Labels["description"]; ok {
		b.WriteString(desc + "\n\n")
	}

	if len(agent.Bins) > 0 {
		b.WriteString("## Binaries\n\n")

		for _, t := range agent.Bins {
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

	if len(agent.Adds) > 0 {
		b.WriteString("## Data Files\n\n")
		b.WriteString("| File | Description |\n")
		b.WriteString("|------|-------------|\n")

		for _, a := range agent.Adds {
			desc := a.Description
			if desc == "" {
				desc = "-"
			}

			fmt.Fprintf(&b, "| %s | %s |\n", filepath.Base(a.Dst), desc)
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
