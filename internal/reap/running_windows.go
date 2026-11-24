//go:build windows

package reap

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is what GetExitCodeProcess answers for a process that has not
// exited.
const stillActive = 259

// Running is the real test.
//
// Windows has no signal 0, so the question is asked of the process itself: open
// it and ask whether it has an exit code yet. A process held by another user
// refuses the open with ERROR_ACCESS_DENIED, which means it is very much alive -
// and that answer must be read as alive rather than as an error, or the reaper
// removes targets belonging to running work. A pid that names no process
// refuses with ERROR_INVALID_PARAMETER.
//
// A process that exited with code 259 reads as still running. That errs the
// safe way: its targets stay standing.
func Running(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
