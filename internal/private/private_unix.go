//go:build !windows

package private

import (
	"fmt"
	"io/fs"
	"os"
)

func mkdir(path string) error {
	return os.Mkdir(path, 0o700)
}

func verify(path string, perm fs.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if got := info.Mode().Perm(); got != perm {
		return fmt.Errorf("%s has mode %o, want %o", path, got, perm)
	}
	return nil
}
