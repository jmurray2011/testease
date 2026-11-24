// Package filemode says whether a file on the runner is meant to be run, for
// the targets it is staged onto.
package filemode

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"runtime"
)

// Executable says whether a regular file is meant to be run. On unix the
// executable bit says so. Windows has no executable bit, so there a file says
// so by what it starts with: a shebang or the ELF magic, which is the intent a
// Linux target can act on. A Windows target does not read modes at all.
func Executable(path string, info fs.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Mode().Perm()&0o111 != 0 {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 4)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	return bytes.HasPrefix(head, []byte("#!")) || bytes.Equal(head, []byte("\x7fELF"))
}
