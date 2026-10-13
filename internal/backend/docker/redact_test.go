package docker

import (
	"strings"
	"testing"
)

func TestLoggedArgumentsKeepEnvironmentKeysAndDropValues(t *testing.T) {
	t.Parallel()

	// A manifest's env: becomes docker exec -e KEY=VALUE, and a scenario run
	// against a real system puts credentials there. A debug log that writes
	// them out has published them to whatever collects CI output.
	args := []string{"exec", "-i", "-e", "VERSION=2.0", "-e", "TOKEN=hunter2", "box", "bash"}
	got := strings.Join(redact(args), " ")

	if strings.Contains(got, "hunter2") || strings.Contains(got, "2.0") {
		t.Errorf("redact kept a value: %q", got)
	}
	for _, want := range []string{"TOKEN", "VERSION", "exec", "box", "bash"} {
		if !strings.Contains(got, want) {
			t.Errorf("redact dropped %q, which is not a secret: %q", want, got)
		}
	}
}

func TestRedactLeavesOrdinaryArgumentsAlone(t *testing.T) {
	t.Parallel()

	args := []string{"run", "-d", "--name", "box", "--label", "testease.run=abc", "img"}
	got := strings.Join(redact(args), " ")
	if got != strings.Join(args, " ") {
		t.Errorf("redact changed a command with no env in it: %q", got)
	}
}
