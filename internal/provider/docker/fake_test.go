package docker

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

// The programs these tests stand in for - the docker CLI, an instrument's
// diff - are faked by the test binary itself, through fakeexec, so the same
// fakes run on every platform. The external docker_test package names them
// by behaviour too: both packages are one test binary.
func TestMain(m *testing.M) {
	fakeexec.Main(m, map[string]fakeexec.Behaviour{
		"stage-mismatch":   fakeStageMismatch,
		"stage-unreadable": fakeStageUnreadable,
		"instrument-diff":  fakeInstrumentDiff,
	})
}

// fakeStageMismatch answers the content-addressed check with a digest other
// than the one the verified input carries.
func fakeStageMismatch(c fakeexec.Call) int {
	switch c.Arg(3) {
	case "test":
		return 0
	case "sha256sum":
		_, _ = fmt.Fprintln(c.Stdout, strings.Repeat("a", 64)+"  /input")
		return 0
	case "inspect":
		_, _ = fmt.Fprintln(c.Stdout, "true")
		return 0
	}
	return 1
}

// fakeStageUnreadable has a mount the container cannot hash: a remote daemon
// bound a directory where the runner's file should be.
func fakeStageUnreadable(c fakeexec.Call) int {
	switch {
	case c.Arg(1) == "exec" && c.Arg(3) == "test":
		return 0
	case c.Arg(1) == "exec" && c.Arg(3) == "sha256sum":
		_, _ = fmt.Fprintln(c.Stderr, "sha256sum: /input: Is a directory")
		return 1
	case c.Arg(1) == "inspect":
		_, _ = fmt.Fprintln(c.Stdout, "true")
		return 0
	}
	return 1
}

// fakeInstrumentDiff is an instrument's diff: exit 0 when the two readings
// match, and otherwise print the lines each has that the other lacks, as diff
// does, and exit 1.
func fakeInstrumentDiff(c fakeexec.Call) int {
	before, errBefore := os.ReadFile(c.Arg(1))
	after, errAfter := os.ReadFile(c.Arg(2))
	if errBefore != nil || errAfter != nil {
		_, _ = fmt.Fprintln(c.Stderr, errBefore, errAfter)
		return 1
	}
	if bytes.Equal(before, after) {
		return 0
	}
	had := lines(before)
	has := lines(after)
	for _, line := range strings.Split(strings.TrimRight(string(before), "\n"), "\n") {
		if !has[line] {
			_, _ = fmt.Fprintln(c.Stdout, "< "+line)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(string(after), "\n"), "\n") {
		if !had[line] {
			_, _ = fmt.Fprintln(c.Stdout, "> "+line)
		}
	}
	return 1
}

func lines(body []byte) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		set[line] = true
	}
	return set
}
