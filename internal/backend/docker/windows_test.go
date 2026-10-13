package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These need a Windows daemon and an image already on it. They pull nothing.
// Set TESTEASE_TEST_WINDOWS_CONTEXT to a docker context whose daemon runs
// Windows containers, and TESTEASE_TEST_WINDOWS_IMAGE to an image present on it.
func requireWindowsDaemon(t *testing.T) Client {
	t.Helper()

	name := os.Getenv("TESTEASE_TEST_WINDOWS_CONTEXT")
	if name == "" {
		t.Skip("no TESTEASE_TEST_WINDOWS_CONTEXT set")
	}
	client := Client{Context: name}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if !client.windows(ctx) {
		t.Skipf("the daemon at context %s does not run Windows containers", name)
	}
	if _, err := client.Output(ctx, "image", "inspect", windowsImage()); err != nil {
		t.Skipf("image %s is not present on %s", windowsImage(), name)
	}
	return client
}

func windowsImage() string {
	if v := os.Getenv("TESTEASE_TEST_WINDOWS_IMAGE"); v != "" {
		return v
	}
	return "mcr.microsoft.com/windows/nanoserver:ltsc2022"
}

// The probe has to ANSWER, not merely be shaped right.
//
// TestTheWindowsExistsProbeQuotesThePath asserts the command's text and passed
// green while this returned "not there" about a file that was there - which is
// the failure this whole tool exists against, turned inward. Staging refused
// every scenario declaring a mount on the Windows daemon, eleven of the twelve
// in the corpus, with the file sitting in the container the whole time.
//
// So this one runs the probe. It needs a real daemon because the defect is in a
// cmd.exe expression and nothing on Linux can evaluate one.
func TestTheWindowsExistsProbeFindsAFileThatIsThere(t *testing.T) {
	client := requireWindowsDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	local := filepath.Join(t.TempDir(), "staged.txt")
	if err := os.WriteFile(local, []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "testease-exists-probe-test"
	_, _ = client.Output(ctx, "rm", "-f", name)
	t.Cleanup(func() {
		_, _ = client.Output(context.WithoutCancel(ctx), "rm", "-f", name)
	})

	// Create, copy, start: docker cp is refused once a Windows container is
	// running, which is the same window Launch stages media in.
	if _, err := client.Output(ctx, "create", "--name", name, windowsImage(), "ping", "-t", "localhost"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := client.Output(ctx, "cp", local, name+":/staged.txt"); err != nil {
		t.Fatalf("cp: %v", err)
	}
	if _, err := client.Output(ctx, "start", name); err != nil {
		t.Fatalf("start: %v", err)
	}

	session := &Session{Client: client, Container: name, OS: Windows}

	there, err := session.Exists(ctx, "/staged.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !there {
		t.Error("Exists said no about a file that was copied in; staging refuses media that arrived")
	}

	absent, err := session.Exists(ctx, "/never-staged.txt")
	if err != nil {
		t.Fatalf("Exists on an absent path: %v", err)
	}
	if absent {
		t.Error("Exists said yes about a file that was never there")
	}
}

// A path with a space is the one the quoting exists for, and it has to answer
// both ways too: unquoted, `if exist C:\staged` becomes the condition and the
// rest the command, so the check passes vacuously on exactly the destinations
// Windows most encourages. The space is in the file name rather than a
// directory because docker cp will not create a destination directory the base
// image does not already have, and the quoting does not care which it is.
func TestTheWindowsExistsProbeAnswersForAPathWithASpace(t *testing.T) {
	client := requireWindowsDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	local := filepath.Join(t.TempDir(), "staged.txt")
	if err := os.WriteFile(local, []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "testease-exists-probe-space-test"
	_, _ = client.Output(ctx, "rm", "-f", name)
	t.Cleanup(func() {
		_, _ = client.Output(context.WithoutCancel(ctx), "rm", "-f", name)
	})

	if _, err := client.Output(ctx, "create", "--name", name, windowsImage(), "ping", "-t", "localhost"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := client.Output(ctx, "cp", local, name+":/staged file.txt"); err != nil {
		t.Fatalf("cp: %v", err)
	}
	if _, err := client.Output(ctx, "start", name); err != nil {
		t.Fatalf("start: %v", err)
	}

	session := &Session{Client: client, Container: name, OS: Windows}

	there, err := session.Exists(ctx, "/staged file.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !there {
		t.Error("Exists said no about a staged file under a path with a space")
	}

	absent, err := session.Exists(ctx, "/never staged.txt")
	if err != nil {
		t.Fatalf("Exists on an absent path: %v", err)
	}
	if absent {
		t.Error("Exists passed vacuously on a path with a space")
	}
}
