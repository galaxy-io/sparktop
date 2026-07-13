// Package cli implements the sparktop command-line interface: a subcommand
// dispatcher with the dashboard as the default action.
package cli

import (
	"fmt"
	"io"
	"os"
)

// Dispatch routes args[1:] to a subcommand and returns the exit code.
// Bare "sparktop" and "sparktop -flag …" run the dashboard.
func Dispatch(args []string) int {
	if len(args) == 0 {
		return Dashboard(nil)
	}
	if args[0] == "" || args[0][0] == '-' {
		return Dashboard(args)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "dashboard":
		return Dashboard(rest)
	case "deploy":
		return Deploy(rest)
	case "teardown":
		return Teardown(rest)
	case "health":
		return Health(rest)
	case "version":
		return Version(rest)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "sparktop: unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: sparktop [command] [flags]

commands:
  dashboard                       run the cluster dashboard (default)
  deploy   <ssh-target>...        install node_exporter + dcgm-exporter on the targets
  teardown <ssh-target>...        stop the exporter stack (--purge also removes remote dir)
  health   <host[:port]>...       probe exporter reachability (TCP + /metrics)
  version                         print version
  help                            show this help

run 'sparktop <command> -h' for command-specific flags.
`)
}
