// Command sparktop is the CLI and live dashboard for a small NVIDIA DGX Spark
// cluster. With no arguments it runs the dashboard; subcommands handle
// deploying/tearing down the exporter stack and probing node health.
package main

import (
	"os"

	"github.com/galaxy-io/sparktop/internal/cli"
)

func main() {
	os.Exit(cli.Dispatch(os.Args[1:]))
}
