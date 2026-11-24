package hyperv

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

// The docker CLI is faked by the test binary itself, through fakeexec, so the
// same fakes run on every platform.
func TestMain(m *testing.M) {
	fakeexec.Main(m, map[string]fakeexec.Behaviour{
		"one-shot-daemon": fakeOneShotDaemon,
		"slow-copy-out":   fakeSlowCopyOut,
	})
}

// fakeOneShotDaemon answers create/cp/start/rm/stop the way the daemon would.
// `cp <cell>:/wanted.txt <local>` hands back a fixed body; a facts file is
// refused, and any other container path is refused the way a file that is
// not there is. `start -a` exits with start-exit and inspect reports the
// container in state with container-exit.
func fakeOneShotDaemon(c fakeexec.Call) int {
	switch c.Arg(1) {
	case "rm", "stop", "create":
		return 0
	case "inspect":
		_, _ = fmt.Fprint(c.Stdout, c.Params["state"]+" "+c.Params["container-exit"])
		return 0
	case "cp":
		src, dst := c.Arg(2), c.Arg(3)
		switch {
		case strings.HasSuffix(src, ":/wanted.txt"):
			if err := os.WriteFile(dst, []byte("kept bytes"), 0o644); err != nil {
				return 1
			}
			return 0
		case isContainerPath(src) && strings.HasSuffix(src, "/testease-facts"):
			return 1
		case isContainerPath(src):
			_, _ = fmt.Fprintln(c.Stderr, "no such file")
			return 1
		}
		return 0
	case "start":
		code, err := strconv.Atoi(c.Params["start-exit"])
		if err != nil {
			return 98
		}
		return code
	}
	return 0
}

// fakeSlowCopyOut never returns from the copy that takes readings OUT of the
// container. Staging the probe IN is not cleanup and is not bounded, so a fake
// that stalled both would be testing the wrong wait.
func fakeSlowCopyOut(c fakeexec.Call) int {
	switch c.Arg(1) {
	case "cp":
		if strings.HasPrefix(c.Arg(2), "cell:") {
			time.Sleep(40 * time.Second)
		}
		return 0
	case "inspect":
		_, _ = fmt.Fprint(c.Stdout, "exited 0")
	}
	return 0
}

// isContainerPath says whether a docker cp operand names a path in a
// container, <container>:<path>, rather than one on the runner. A Windows
// runner path has a colon too, after its one-letter drive.
func isContainerPath(operand string) bool {
	i := strings.Index(operand, ":")
	return i > 1
}

// fakeDocker is a docker whose container ran and reported startExit, which is
// the ordinary case.
func fakeDocker(t *testing.T, startExit int) string {
	t.Helper()
	return fakeDockerIn(t, startExit, "exited", startExit)
}

// fakeDockerIn is a docker whose `start -a` exits with one code and whose
// container is left in a given state. The two are separate because they are
// separate on a real daemon: `docker start -a` exits with the CONTAINER's
// status when the container ran, and with its own when the container never
// started, and only the state says which happened.
func fakeDockerIn(t *testing.T, startExit int, state string, containerExit int) string {
	t.Helper()
	return fakeexec.Program(t, "docker", "one-shot-daemon", map[string]string{
		"start-exit":     strconv.Itoa(startExit),
		"state":          state,
		"container-exit": strconv.Itoa(containerExit),
	})
}

// slowCopyDocker is a docker whose `cp` never returns inside the bound, so the
// readings a finished cell holds cannot be taken out of it.
func slowCopyDocker(t *testing.T) string {
	t.Helper()
	return fakeexec.Program(t, "docker", "slow-copy-out", nil)
}
