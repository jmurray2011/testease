package ecs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	shellOnce sync.Once
	shellPath string
	shellErr  error
)

// posixShell is a POSIX sh to run the Fargate wrapper under, which in
// production runs inside a Linux container. Windows rarely has sh on PATH, but
// Git for Windows ships one beside git, so a machine that can check out this
// repository can run the wrapper.
func posixShell(t *testing.T) string {
	t.Helper()
	shellOnce.Do(func() {
		if path, err := exec.LookPath("sh"); err == nil {
			shellPath = path
			return
		}
		out, err := exec.Command("git", "--exec-path").Output()
		if err != nil {
			shellErr = err
			return
		}
		// <git>/mingw64/libexec/git-core -> <git>/bin/sh.exe
		root := filepath.Join(filepath.FromSlash(strings.TrimSpace(string(out))), "..", "..", "..")
		for _, candidate := range []string{
			filepath.Join(root, "bin", "sh.exe"),
			filepath.Join(root, "usr", "bin", "sh.exe"),
		} {
			if _, err := os.Stat(candidate); err == nil {
				shellPath = candidate
				return
			}
		}
		shellErr = os.ErrNotExist
	})
	if shellPath == "" {
		t.Fatalf("no POSIX sh on PATH or beside git to run the Fargate wrapper under: %v", shellErr)
	}
	return shellPath
}
