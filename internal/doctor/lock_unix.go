//go:build unix

package doctor

import (
	"os"
	"syscall"
)

// lockExclusive is the lock the record takes, taken here to find out whether it
// can be. Copied rather than imported: record imports nothing internal and
// exports no lock, and one syscall is cheaper than a seam through the boundary.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
