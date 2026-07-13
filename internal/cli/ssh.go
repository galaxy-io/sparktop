package cli

import (
	"io"
	"os"
	"os/exec"
)

// remoteDir is the directory on each Spark node where the compose file lives.
const remoteDir = "sparktop-exporters"

// sshRun runs a single command on target over ssh, streaming its output.
func sshRun(target, remoteCmd string) error {
	c := exec.Command("ssh", target, remoteCmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// sshPipe runs remoteCmd on target with stdin fed from r.
func sshPipe(target, remoteCmd string, r io.Reader) error {
	c := exec.Command("ssh", target, remoteCmd)
	c.Stdin = r
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
