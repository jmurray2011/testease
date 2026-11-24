//go:build !windows

package campaign

import (
	"os"
	"os/exec"
)

// asStoppable makes cancelling the command ask it to stop, the way Ctrl+C
// does, rather than kill it.
func asStoppable(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
}
