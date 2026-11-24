//go:build unix

package reap

import "syscall"

// Running is the real test.
//
// Signal 0 asks "could I signal this" without signalling. A pid held by another
// user answers EPERM, which means the process is very much alive - and that
// answer must be read as alive rather than as an error, or the reaper removes
// targets belonging to running work.
func Running(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
