//go:build !unix

package record

import (
	"errors"
	"os"
)

// lockExclusive refuses rather than pretending. One writer per events file is
// what makes sequence numbers mean anything, so a platform that cannot enforce
// it must not silently write a record nobody can trust.
func lockExclusive(*os.File) error {
	return errors.New("record locking is not implemented on this platform")
}
