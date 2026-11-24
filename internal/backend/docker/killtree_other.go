//go:build !windows

package docker

import "os/exec"

// stopTree ends docker. A helper it leaves behind cannot hold up the look:
// the output grace closes docker's output regardless.
func stopTree(cmd *exec.Cmd) error { return cmd.Process.Kill() }
