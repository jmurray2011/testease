package hyperv

import (
	"context"

	docker "github.com/jmurray2011/testease/internal/backend/docker"
)

// CleanupBudget bounds work that outlives the caller's context. It is docker's
// own, because both backends drive the same CLI and a cell that stops on one
// timetable and is removed on another would be two answers to one question.
const CleanupBudget = docker.CleanupBudget

// cleanupContext detaches from the caller's cancellation and keeps a deadline.
//
// Detaching is deliberate: a ctrl-C must not stop us removing what we made,
// nor throw away readings a finished cell is still holding. But
// context.WithoutCancel strips the DEADLINE along with the cancellation, and
// against a daemon that accepts a connection and then stops answering that
// left the interrupt path hanging forever.
//
// What a timeout costs differs by the kind of work it wraps. A removal that
// expires leaves a target standing, which is what `testease reap` exists for
// and is a visible, recoverable state. A reading that expires is evidence
// lost, so those say so in the result rather than coming back quietly short.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return docker.CleanupContext(ctx)
}
