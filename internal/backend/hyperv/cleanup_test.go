package hyperv

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	dockerbackend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/target"
)

// context.WithoutCancel STRIPS THE DEADLINE AS WELL AS THE CANCELLATION.
//
// Detaching cleanup from the caller's context is right - a ctrl-C must not
// stop us removing what we made, nor throw away readings already taken - but
// it left `docker rm -f` and friends with no bound at all. Against a daemon
// that accepts the connection and then stops answering, that is the interrupt
// path hanging forever.
func TestCleanupOutlivesCancellationAndStillHasABound(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(context.Background())
	cancel()

	ctx, done := cleanupContext(parent)
	defer done()

	if err := ctx.Err(); err != nil {
		t.Fatalf("cleanup context is already %v; a cancelled run must still be able to clean up", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("cleanup context carries no deadline: a wedged daemon would hold the interrupt path open")
	}
	if left := time.Until(deadline); left <= 0 || left > CleanupBudget+time.Second {
		t.Errorf("deadline in %v, want about %v", left, CleanupBudget)
	}
}

// The budget has to outlast docker's own stop grace, or the bound would kill
// the very call it is meant to let finish.
func TestTheCleanupBudgetOutlastsDockersOwnStopGrace(t *testing.T) {
	t.Parallel()

	if CleanupBudget <= 10*time.Second {
		t.Errorf("CleanupBudget = %v, and `docker stop -t 10` needs longer than that", CleanupBudget)
	}
}

// EVIDENCE IS NOT CLEANUP. Readings are taken through the same detached
// context, and a bound on those can lose a cell's facts - so when it expires
// the cell says so rather than coming back quietly short.
func TestReadingsLostToTheBoundAreReportedNotDropped(t *testing.T) {
	t.Parallel()

	shot := OneShot{
		// A docker whose `cp` never returns: the readings cannot be taken.
		Client: dockerbackend.Client{Bin: slowCopyDocker(t)},
		OS:     Linux,
		Name:   "cell",
		Image:  "neutral:1",
	}

	result, err := shot.Run(t.Context(), []byte("true\n"), nil, target.RunOpts{
		Stdout: io.Discard, Stderr: io.Discard, Structured: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("the cell's readings were not taken and nothing in the result says so")
	}
	if !strings.Contains(strings.ToLower(strings.Join(result.Warnings, " ")), "reading") {
		t.Errorf("warnings = %v, want them to name the readings that were not taken", result.Warnings)
	}
}
