package cli

import (
	"flag"
	"fmt"
	"os"
)

// Teardown stops the exporter stack on each target, optionally purging the dir.
func Teardown(args []string) int {
	fs := flag.NewFlagSet("teardown", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete the remote ~/"+remoteDir+" directory")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: sparktop teardown [--purge] <ssh-target> [<ssh-target> ...]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	targets := fs.Args()
	if len(targets) == 0 {
		fs.Usage()
		return 2
	}

	failed := 0
	for _, t := range targets {
		fmt.Printf("==> [%s] stopping exporter stack\n", t)
		if err := sshRun(t, fmt.Sprintf("cd ~/%s && docker compose down", remoteDir)); err != nil {
			fmt.Fprintf(os.Stderr, "    (compose down failed: %v — stack may already be gone)\n", err)
		}
		if *purge {
			fmt.Printf("==> [%s] removing ~/%s\n", t, remoteDir)
			if err := sshRun(t, fmt.Sprintf("rm -rf ~/%s", remoteDir)); err != nil {
				fmt.Fprintf(os.Stderr, "    purge failed: %v\n", err)
				failed++
			}
		}
		fmt.Println()
	}
	if failed > 0 {
		return 1
	}
	fmt.Println("Done.")
	return 0
}
