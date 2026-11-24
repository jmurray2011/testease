package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// shippedExecutable says whether a probe in this repository carries its
// executable bit. A unix checkout shows the bit on the file itself. Windows has
// no executable bit, so there the mode git records for the file is read
// instead: that mode is what a unix checkout of the same commit gets.
func shippedExecutable(t *testing.T, path string, info os.FileInfo) bool {
	t.Helper()
	if runtime.GOOS != "windows" {
		return info.Mode()&0o111 != 0
	}
	out, err := exec.Command("git", "-C", filepath.Dir(path), "ls-files", "-s", "--", filepath.Base(path)).Output()
	if err != nil {
		t.Fatalf("read the mode git records for %s: %v", path, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatalf("git records no mode for %s: it is not tracked", path)
	}
	return fields[0] == "100755"
}
