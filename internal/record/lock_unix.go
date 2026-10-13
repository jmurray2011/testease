//go:build unix

package record

import (
	"os"
	"syscall"
)

// lockExclusive takes a non-blocking exclusive flock, which the kernel
// releases when the descriptor closes - including on a hard process death, so
// a crashed run never leaves a record locked forever.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
