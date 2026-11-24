//go:build windows

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A docker given up on takes its children with it: an ssh left waiting for a
// password nobody will type would otherwise outlive every look that started
// one.
func TestGivingUpOnADaemonEndsWhatDockerStarted(t *testing.T) {
	t.Parallel()

	pidfile := filepath.Join(t.TempDir(), "child.pid")
	c := fakeDocker(t, "hangs-with-child", map[string]string{"pidfile": pidfile})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Labelled(ctx, "testease.runner"); err == nil {
		t.Fatal("a daemon that never answered was read as empty")
	}
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("the fake never started its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for running(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("docker's child %d is still running", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func running(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h) //nolint:errcheck // a handle we only read through
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}
