package reap_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/jmurray2011/testease/internal/reap"
)

func TestRunningSeesThisProcess(t *testing.T) {
	t.Parallel()

	if !reap.Running(os.Getpid()) {
		t.Error("Running(own pid) = false")
	}
}

func TestRunningRefusesPidsThatNameNoProcess(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1} {
		if reap.Running(pid) {
			t.Errorf("Running(%d) = true", pid)
		}
	}
}

// The proof reap acts on: a process that has exited is gone. A check that
// cannot tell would either keep every target forever or, worse, call a live
// run dead.
func TestRunningSeesAnExitedProcessAsGone(t *testing.T) {
	t.Parallel()

	child := exec.Command(os.Args[0], "-test.run=^$")
	if err := child.Run(); err != nil {
		t.Fatalf("run a child to exit: %v", err)
	}
	if reap.Running(child.Process.Pid) {
		t.Errorf("Running(%d) = true after the child exited", child.Process.Pid)
	}
}
