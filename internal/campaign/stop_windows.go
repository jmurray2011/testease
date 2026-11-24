//go:build windows

package campaign

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// asStoppable makes cancelling the command ask it to stop rather than kill
// it. Windows has no signals: testease reads Ctrl+Break as an interrupt, so
// the run gets a process group of its own and the break is sent to that
// group alone, over the console the two share. Ctrl+C is off in a new group,
// so a person's Ctrl+C reaches this process, which passes it on as a break.
func asStoppable(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
