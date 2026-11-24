package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/target/fakeexec"
)

func fakeExecStreamsThenSleeps(c fakeexec.Call) int {
	if c.Arg(1) == "exec" {
		_, _ = fmt.Fprint(c.Stdout, "first")
		time.Sleep(10 * time.Second)
	}
	return 0
}

func TestFetchReturnsAStreamBeforeTheRemoteCommandCompletes(t *testing.T) {
	t.Parallel()

	client := fakeDocker(t, "exec-streams-then-sleeps", nil)
	session := &Session{Client: client, Container: "box"}
	// The remote command takes ten seconds. Fetch returning well inside that
	// is a stream; a fixed tenth of a second measured process start on a
	// loaded Windows runner as much as streaming, and failed there at 0.6 s.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	started := time.Now()
	reader, err := session.Fetch(ctx, "/large.dump")
	if err != nil {
		t.Fatalf("Fetch waited for command completion: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Fetch returned after %s, want a live stream immediately", elapsed)
	}

	body := make([]byte, 5)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("read first bytes: %v", err)
	}
	if string(body) != "first" {
		t.Fatalf("first bytes = %q", body)
	}
	cancel()
	_ = reader.Close()
}

func fakeExecFailsLate(c fakeexec.Call) int {
	if c.Arg(1) == "exec" {
		_, _ = fmt.Fprint(c.Stdout, "partial")
		_, _ = fmt.Fprintln(c.Stderr, "remote-broke")
		return 42
	}
	return 0
}

func TestFetchSurfacesALateRemoteFailureFromTheReader(t *testing.T) {
	t.Parallel()

	client := fakeDocker(t, "exec-fails-late", nil)
	session := &Session{Client: client, Container: "box"}
	reader, err := session.Fetch(context.Background(), "/partial.dump")
	if err != nil {
		t.Fatalf("Fetch should return the stream before its final status: %v", err)
	}
	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(reader)
	if string(body) != "partial" {
		t.Errorf("body = %q", body)
	}
	if err == nil || !strings.Contains(err.Error(), "remote-broke") {
		t.Fatalf("read error = %v, want the late Docker failure and stderr", err)
	}
}
