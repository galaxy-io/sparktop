package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/galaxy-io/sparktop/internal/exporters"
)

// Deploy uploads the exporter compose file to each target and brings it up.
func Deploy(args []string) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: sparktop deploy <ssh-target> [<ssh-target> ...]\n")
		fmt.Fprint(os.Stderr, "example: sparktop deploy me@spark-01 me@spark-02\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	targets := fs.Args()
	if len(targets) == 0 {
		fs.Usage()
		return 2
	}

	var failed []string
	for _, t := range targets {
		if err := deployOne(t); err != nil {
			fmt.Fprintf(os.Stderr, "==> [%s] FAILED: %v\n\n", t, err)
			failed = append(failed, t)
			continue
		}
		fmt.Println()
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "deploy failed for %d target(s): %v\n", len(failed), failed)
		return 1
	}
	fmt.Println("Done. Configure sparktop to point at <host>:9100 and <host>:9400 for each node.")
	return 0
}

func deployOne(target string) error {
	fmt.Printf("==> [%s] preparing remote dir ~/%s\n", target, remoteDir)
	if err := sshRun(target, fmt.Sprintf("mkdir -p ~/%s", remoteDir)); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	fmt.Printf("==> [%s] uploading docker-compose.yml\n", target)
	if err := sshPipe(target,
		fmt.Sprintf("cat > ~/%s/docker-compose.yml", remoteDir),
		bytes.NewReader(exporters.ComposeYAML),
	); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	fmt.Printf("==> [%s] pulling images + starting\n", target)
	if err := sshRun(target, fmt.Sprintf("cd ~/%s && docker compose pull --quiet && docker compose up -d", remoteDir)); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	fmt.Printf("==> [%s] status\n", target)
	return sshRun(target, fmt.Sprintf("cd ~/%s && docker compose ps", remoteDir))
}
