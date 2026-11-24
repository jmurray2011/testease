package campaign

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

const childEnv = "TESTEASE_CAMPAIGN_TEST_CHILD"

// TestMain lets the test binary stand in for a testease run: one that, told
// to stop, closes its record and exits 12 the way a real run does.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "interruptible" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		_, _ = os.Stdout.WriteString("ready\n")
		select {
		case <-ctx.Done():
			os.Exit(12)
		case <-time.After(time.Minute):
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

type readyWriter struct{ ready chan struct{} }

func (w *readyWriter) Write(p []byte) (int, error) {
	select {
	case <-w.ready:
	default:
		close(w.ready)
	}
	return len(p), nil
}

// A batch or campaign that is stopped asks its runs to stop rather than
// killing them: a killed run cannot close its record, and a record that just
// ends reads as one that finished.
func TestStoppingAnInvocationLetsTheRunCloseItsRecord(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &readyWriter{ready: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- ExecInvoker{Executable: self, Env: append(os.Environ(), childEnv+"=interruptible")}.Invoke(ctx, out, io.Discard)
	}()
	select {
	case <-out.ready:
	case <-time.After(30 * time.Second):
		t.Fatal("the child never started")
	}
	cancel()
	select {
	case code := <-done:
		if code != 12 {
			t.Errorf("exit %d, want 12: the run was killed rather than asked to stop", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the child did not stop")
	}
}
