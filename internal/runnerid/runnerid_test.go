package runnerid_test

import (
	"os"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/runnerid"
)

func TestUserIsUSERWhereItIsSet(t *testing.T) {
	t.Setenv("USER", "operator")
	t.Setenv("USERNAME", "someone-else")

	if got := runnerid.User(); got != "operator" {
		t.Errorf("User() = %q, want operator", got)
	}
}

// Windows names the account in USERNAME and leaves USER unset. Without the
// fallback every Windows runner is "@host": two people on one machine look
// like one owner, and reap would offer one of them the other's targets.
func TestUserFallsBackToUSERNAME(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "operator")

	if got := runnerid.User(); got != "operator" {
		t.Errorf("User() = %q, want operator", got)
	}
}

func TestRunnerIsUserAtHost(t *testing.T) {
	t.Setenv("USER", "operator")
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname: %v", err)
	}

	got := runnerid.Runner()
	if got != "operator@"+host {
		t.Errorf("Runner() = %q, want operator@%s", got, host)
	}
	if strings.Count(got, "@") != 1 {
		t.Errorf("Runner() = %q, want exactly one @", got)
	}
}
