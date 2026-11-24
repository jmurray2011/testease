//go:build !windows

package main

import (
	"os"
	"os/exec"
)

func prepare(*exec.Cmd) {}

// interrupt sends the child SIGINT, as Ctrl+C in its terminal would.
func interrupt(cmd *exec.Cmd) error {
	return cmd.Process.Signal(os.Interrupt)
}
