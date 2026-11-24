//go:build windows

package doctor

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive is the lock the record takes, taken here to find out whether it
// can be. Copied rather than imported, as on unix: the same non-blocking
// exclusive LockFileEx on the same byte at the end of the addressable range.
func lockExclusive(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1, 0,
		&windows.Overlapped{Offset: 0xFFFFFFFF, OffsetHigh: 0x7FFFFFFF},
	)
}
