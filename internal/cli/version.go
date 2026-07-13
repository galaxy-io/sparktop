package cli

import "fmt"

// Set via -ldflags "-X github.com/galaxy-io/sparktop/internal/cli.version=..."
// (and .commit / .buildDate) by the Taskfile and goreleaser.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// Version prints the build version.
func Version(args []string) int {
	out := "sparktop " + version
	if commit != "" {
		out += fmt.Sprintf(" (%s", commit)
		if buildDate != "" {
			out += ", " + buildDate
		}
		out += ")"
	}
	fmt.Println(out)
	return 0
}
