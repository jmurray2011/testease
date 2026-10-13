package ssh

import (
	"bytes"
	"sync"
	"testing"
)

// A BACKEND MUST NOT WRITE INTO THE CALLER'S BUFFERS AFTER IT HAS RETURNED.
//
// The runner hands its own tail writers to the session, then reads them for
// the step's claim and closes the log the moment Run returns. On a timeout
// this backend gives the transport 10 seconds to unblock and returns anyway -
// deliberately, because a wedged transport must not hold the runner forever -
// and the goroutine still inside sess.Run keeps writing into those writers.
// That is a data race on the buffer the claim is selected from, and it happens
// exactly when a run is already going wrong.
func TestADetachedWriterStopsWritingIntoTheCallersBuffer(t *testing.T) {
	t.Parallel()

	var caller bytes.Buffer
	w := detachable(&caller)

	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.detach()
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatalf("Write after detach should be accepted and dropped: %v", err)
	}

	if got := caller.String(); got != "before\n" {
		t.Errorf("caller saw %q, want only what was written before it detached", got)
	}
}

// The race the detach exists to prevent, under -race: a goroutine writing
// while the owner detaches and then reads.
func TestDetachingRacesNothing(t *testing.T) {
	t.Parallel()

	var caller bytes.Buffer
	w := detachable(&caller)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_, _ = w.Write([]byte("late\n"))
		}
	}()

	w.detach()
	_ = caller.Len()
	wg.Wait()
}

// A nil writer stays nil: the session leaves the stream alone rather than
// being handed something that discards it.
func TestNothingToDetachFromIsLeftAlone(t *testing.T) {
	t.Parallel()

	if w := detachable(nil); w != nil {
		t.Errorf("detachable(nil) = %v, want nil so the session keeps its own default", w)
	}
}
