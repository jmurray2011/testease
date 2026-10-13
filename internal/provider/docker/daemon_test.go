package docker_test

import (
	"strings"
	"testing"

	provider "github.com/jmurray2011/testease/internal/provider/docker"
)

// A record of a run against --docker-context winhost read exactly like a
// record of a local one: nothing in it named the daemon that held the box.
// host.hostname is the runner's, not the target's, so which machine the
// measurement happened on was recoverable only by inference from a kernel
// fact - and reap, which needs the context to find what was left standing,
// could not learn it from the record at all.
func TestATargetNamesTheDaemonThatHeldIt(t *testing.T) {
	t.Parallel()

	_, tgt, _ := boot(t, provider.Options{})
	id := tgt.Identity()

	daemon, _ := id.Resolved["daemon"].(string)
	if strings.TrimSpace(daemon) == "" {
		t.Errorf("resolved = %v, want the daemon that held the box named", id.Resolved)
	}
	// The context is recorded even when it is the default one. Absent, a
	// reader cannot tell "the default context" from "nobody wrote it down".
	if _, ok := id.Resolved["docker_context"]; !ok {
		t.Errorf("resolved = %v, want the docker context recorded, default included", id.Resolved)
	}
}
