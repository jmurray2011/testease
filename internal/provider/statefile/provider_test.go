package statefile_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/provider/statefile"
	"github.com/jmurray2011/testease/internal/target"
)

// testease consumes targets and does not provision them. This is where that
// boundary is drawn: somebody else brings an environment up and writes down
// what they made.

func written(t *testing.T, name string, state statefile.State) string {
	t.Helper()

	dir := t.TempDir()
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func TestAStateFileThatIsNotThere(t *testing.T) {
	t.Parallel()

	p := statefile.New(statefile.Options{Dir: t.TempDir(), Profile: "lab"})
	_, err := p.Resolve(context.Background(), target.Spec{Host: "box", Address: "nothing"})
	if err == nil {
		t.Fatal("resolved an environment nobody provisioned")
	}
	if !strings.Contains(err.Error(), "nothing.json") {
		t.Errorf("error %q does not name the file it looked for", err)
	}
}

func TestAStateFileNamingNoInstance(t *testing.T) {
	t.Parallel()

	dir := written(t, "env", statefile.State{Name: "env", Product: "thing"})
	p := statefile.New(statefile.Options{Dir: dir, Profile: "lab"})

	_, err := p.Resolve(context.Background(), target.Spec{Host: "box", Address: "env"})
	if err == nil {
		t.Fatal("resolved a state file describing no machine")
	}
	if !strings.Contains(err.Error(), "nothing to dial") {
		t.Errorf("error = %q", err)
	}
}

func TestTheProvisionersOwnFieldNamesSurvive(t *testing.T) {
	t.Parallel()

	// Renaming them here would put a translation between the file and the
	// reader, and the first time the two drifted the record would say
	// something the provisioner never said.
	var state statefile.State
	body := `{"name":"env-1","env_id":"e-42","instance_id":"i-abc","local_port":18080,
	          "forward_pid":900,"blueprint":"small","product":"thing","version":"3.0",
	          "origin":"http://127.0.0.1:18080"}`
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if state.InstanceID != "i-abc" || state.LocalPort != 18080 || state.Version != "3.0" {
		t.Errorf("state = %+v", state)
	}
}
