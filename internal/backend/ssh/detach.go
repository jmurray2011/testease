package ssh

import (
	"io"
	"sync"
)

// detachedWriter forwards to a writer somebody else owns, until told to stop.
//
// This exists for one measured shape. A step's streams belong to the runner:
// it reads them for the claim and closes the log as soon as Run returns. When
// a transport wedges, Run signals the far side, closes the session, waits a
// bounded 10 seconds and returns anyway - and the goroutine still inside
// sess.Run goes on writing into those streams. Detaching first makes the late
// output go nowhere instead of into a buffer the runner is concurrently
// reading and closing.
//
// The goroutine is still leaked; that is a transport that will not unblock,
// and there is nothing here that can free it. What this fixes is where its
// output lands.
type detachedWriter struct {
	mu sync.Mutex
	to io.Writer
}

// detachable wraps a writer the caller owns. Nil in, nil out: a stream nobody
// supplied is left for the session to handle its own way rather than being
// handed a writer that swallows it.
func detachable(to io.Writer) *detachedWriter {
	if to == nil {
		return nil
	}
	return &detachedWriter{to: to}
}

// Write forwards while attached, and reports the bytes as written once it is
// not. A short write would read to the far side as a broken stream, which is a
// different failure from the one that actually happened.
func (w *detachedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.to == nil {
		return len(p), nil
	}
	return w.to.Write(p)
}

// detach stops forwarding. After it returns, the writer underneath is not
// touched again by anything holding this.
func (w *detachedWriter) detach() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.to = nil
}
