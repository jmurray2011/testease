//go:build unix

package record

import (
	"errors"
	"os"
	"syscall"
)

// lockExclusive takes a non-blocking exclusive flock, which the kernel
// releases when the descriptor closes - including on a hard process death, so
// a crashed run never leaves a record locked forever.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// lockContended says the lock failed because another writer holds it, as
// distinct from a filesystem that cannot lock at all.
func lockContended(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
