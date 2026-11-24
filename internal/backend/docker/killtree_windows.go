//go:build windows

package docker

import (
	"os/exec"
	"strconv"
)

// stopTree ends docker and everything it started. Killing docker alone leaves
// its ssh helper behind when a context is reached over ssh, still waiting for
// a password, one more for every look.
func stopTree(cmd *exec.Cmd) error {
	pid := strconv.Itoa(cmd.Process.Pid)
	_ = exec.Command("taskkill", "/T", "/F", "/PID", pid).Run() //nolint:gosec // a pid this process started
	return cmd.Process.Kill()
}
