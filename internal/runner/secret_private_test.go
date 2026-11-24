package runner

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/backend/private"
	"github.com/jmurray2011/testease/internal/scenario"
)

// A secret workspace holds credentials for the length of a run. Nobody but
// the account running testease may read it, on any platform: on Windows that
// is an access list, because modes are not kept there.
func TestASecretWorkspaceIsOnlyItsOwners(t *testing.T) {
	t.Parallel()

	r := &run{
		opt:   Options{RunsDir: t.TempDir()},
		plan:  &scenario.Plan{Secrets: map[string]scenario.SecretFile{"actors": {}}},
		runID: "secret-20260902T120000Z",
	}
	if err := r.prepareSecretFiles(); err != nil {
		t.Fatalf("prepareSecretFiles: %v", err)
	}
	defer r.cleanupSecretFiles()

	if err := private.Verify(r.secretDir, 0o700); err != nil {
		t.Errorf("secret workspace: %v", err)
	}
	for name, perm := range map[string]fs.FileMode{"owner.json": 0o600, "actors": 0o600} {
		if err := private.Verify(filepath.Join(r.secretDir, name), perm); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
