//go:build windows

package record

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive takes a non-blocking exclusive LockFileEx, which Windows
// releases when the handle closes - including on a hard process death, so a
// crashed run never leaves a record locked forever.
//
// A Windows lock covers bytes, and a locked byte cannot be read through any
// other handle. Locking the events themselves would stop judge, render and
// follow reading a live run. So the lock covers one byte at the very end of
// the addressable range, which no record will ever reach: it excludes other
// writers, who ask for the same byte, and nothing else.
func lockExclusive(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1, 0,
		&windows.Overlapped{Offset: 0xFFFFFFFF, OffsetHigh: 0x7FFFFFFF},
	)
}

// lockContended says the lock failed because another writer holds it, as
// distinct from a filesystem that cannot lock at all.
func lockContended(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
