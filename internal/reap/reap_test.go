package reap_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/reap"
)

// Reaping runs the ownership rule backwards, so the inverse has to be exactly
// as precise as the forward one. Too greedy and it deletes somebody's
// long-lived box; too shy and targets leak until somebody notices the disk.
//
// Not a heuristic and not an age window: a target whose owning process is gone
// cannot belong to a running run. Nothing else counts as proof.

func labels(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

func mine() reap.Runner { return reap.Runner{User: "jo", Host: "labhost"} }

// gone says every pid is dead; here says every pid is alive.
func gone(int) bool { return false }
func here(int) bool { return true }

func TestATargetWhoseProcessIsGoneIsOrphaned(t *testing.T) {
	t.Parallel()

	fate, why := reap.Decide(labels(
		"testease.runner", "jo@labhost",
		"testease.pid", "41922",
		"testease.run", "sweep-20260812T140322Z",
	), mine(), gone)

	if fate != reap.Orphaned {
		t.Errorf("fate = %q (%s), want %q", fate, why, reap.Orphaned)
	}
}

func TestATargetWhoseProcessIsStillRunningIsLeftAlone(t *testing.T) {
	t.Parallel()

	// A false positive here is the reaper deleting a target belonging to a run
	// that is still going.
	fate, why := reap.Decide(labels(
		"testease.runner", "jo@labhost",
		"testease.pid", "41922",
	), mine(), here)

	if fate != reap.Live {
		t.Errorf("fate = %q (%s), want %q", fate, why, reap.Live)
	}
}

func TestAnotherUsersTargetIsNotOursToJudge(t *testing.T) {
	t.Parallel()

	// kill -0 on a pid held by another user answers EPERM, which is still
	// alive - and still not ours. The name settles it before the pid is asked.
	fate, why := reap.Decide(labels(
		"testease.runner", "someone-else@labhost",
		"testease.pid", "41922",
	), mine(), gone)

	if fate != reap.Foreign {
		t.Errorf("fate = %q (%s), want %q", fate, why, reap.Foreign)
	}
}

func TestATargetFromAnotherMachineCannotBeProvedAnything(t *testing.T) {
	t.Parallel()

	// A pid means something on the machine that issued it and nothing at all
	// anywhere else. Reading one from here would be reading a stranger's
	// process table by coincidence of number.
	fate, why := reap.Decide(labels(
		"testease.runner", "jo@another-host",
		"testease.pid", "41922",
	), mine(), gone)

	if fate != reap.Unprovable {
		t.Errorf("fate = %q, want %q", fate, reap.Unprovable)
	}
	if !strings.Contains(why, "another-host") {
		t.Errorf("why = %q, want it to name the machine it came from", why)
	}
}

func TestSomethingTestEaseNeverMadeIsLeftAlone(t *testing.T) {
	t.Parallel()

	for _, l := range []map[string]string{
		{},
		labels("com.example.something", "else"),
		labels("testease.runner", "jo@labhost"),
		labels("testease.pid", "41922"),
	} {
		if fate, _ := reap.Decide(l, mine(), gone); fate != reap.Unprovable {
			t.Errorf("labels %v = %q, want %q", l, fate, reap.Unprovable)
		}
	}
}

func TestAPidThatIsNotANumberProvesNothing(t *testing.T) {
	t.Parallel()

	fate, _ := reap.Decide(labels(
		"testease.runner", "jo@labhost",
		"testease.pid", "not-a-pid",
	), mine(), gone)

	if fate != reap.Unprovable {
		t.Errorf("fate = %q, want %q", fate, reap.Unprovable)
	}
}

func TestPidsCompareWholeNeverAsAPrefix(t *testing.T) {
	t.Parallel()

	// 4242 and 42423 are different runs, and one ending must not take the
	// other's targets with it.
	alive := func(pid int) bool { return pid == 42423 }

	if fate, _ := reap.Decide(labels("testease.runner", "jo@labhost", "testease.pid", "4242"), mine(), alive); fate != reap.Orphaned {
		t.Errorf("pid 4242 = %q, want %q", fate, reap.Orphaned)
	}
	if fate, _ := reap.Decide(labels("testease.runner", "jo@labhost", "testease.pid", "42423"), mine(), alive); fate != reap.Live {
		t.Errorf("pid 42423 = %q, want %q", fate, reap.Live)
	}
}

func TestWideningToEveryUserStillNeedsTheSameMachine(t *testing.T) {
	t.Parallel()

	all := reap.Runner{User: "jo", Host: "labhost", AllUsers: true}

	if fate, _ := reap.Decide(labels("testease.runner", "someone-else@labhost", "testease.pid", "41922"), all, gone); fate != reap.Orphaned {
		t.Errorf("another user on this machine = %q, want %q with --all-users", fate, reap.Orphaned)
	}
	// Widening to every user does not widen to every machine: a pid from
	// somewhere else is still meaningless here.
	if fate, _ := reap.Decide(labels("testease.runner", "jo@another-host", "testease.pid", "41922"), all, gone); fate != reap.Unprovable {
		t.Errorf("another machine = %q, want %q even with --all-users", fate, reap.Unprovable)
	}
}

func TestAgeIsNeverTheReason(t *testing.T) {
	t.Parallel()

	// "It looks old" is exactly the reasoning this replaces. A target created
	// a year ago whose process is still running is still in use.
	old := labels(
		"testease.runner", "jo@labhost",
		"testease.pid", "41922",
		"testease.created", time.Now().AddDate(-1, 0, 0).UTC().Format(time.RFC3339),
	)
	if fate, _ := reap.Decide(old, mine(), here); fate != reap.Live {
		t.Errorf("fate = %q, want %q: age is not evidence", fate, reap.Live)
	}
}

func TestTheReasonAlwaysSaysWhy(t *testing.T) {
	t.Parallel()

	for _, l := range []map[string]string{
		labels("testease.runner", "jo@labhost", "testease.pid", "1"),
		labels("testease.runner", "other@labhost", "testease.pid", "1"),
		labels("testease.runner", "jo@elsewhere", "testease.pid", "1"),
		{},
	} {
		if _, why := reap.Decide(l, mine(), gone); strings.TrimSpace(why) == "" {
			t.Errorf("labels %v gave no reason", l)
		}
	}
}
