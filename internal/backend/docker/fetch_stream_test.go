package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func fakeExecStreamsThenSleeps(c fakeCall) int {
	if c.arg(1) == "exec" {
		_, _ = fmt.Fprint(c.stdout, "first")
		time.Sleep(10 * time.Second)
	}
	return 0
}

func TestFetchReturnsAStreamBeforeTheRemoteCommandCompletes(t *testing.T) {
	t.Parallel()

	client := fakeDocker(t, "exec-streams-then-sleeps", nil)
	session := &Session{Client: client, Container: "box"}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	started := time.Now()
	reader, err := session.Fetch(ctx, "/large.dump")
	if err != nil {
		t.Fatalf("Fetch waited for command completion: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
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

func fakeExecFailsLate(c fakeCall) int {
	if c.arg(1) == "exec" {
		_, _ = fmt.Fprint(c.stdout, "partial")
		_, _ = fmt.Fprintln(c.stderr, "remote-broke")
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
