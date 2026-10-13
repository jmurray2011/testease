package runner_test

import (
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/target"
)

// A backend that came back short of a cell's readings says so, and the runner
// puts that into the record rather than leaving it in the result nobody keeps.
func TestABackendsOwnWarningReachesTheRecord(t *testing.T) {
	t.Parallel()

	session := &fakeSession{run: func(_ []byte, _ []string, _ target.RunOpts) (target.Result, error) {
		return target.Result{RC: 0, Warnings: []string{"the readings could not be taken out within 30s"}}, nil
	}}
	_, rec, err := exec(t, plan(t, oneStep), session, runOpts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var said []string
	for _, e := range rec.Events {
		if e.Kind != record.KindRecordWarning {
			continue
		}
		said = append(said, string(e.Data))
	}
	if len(said) == 0 {
		t.Fatal("the backend said it lost readings and the record does not mention it")
	}
	if !strings.Contains(strings.Join(said, " "), "could not be taken out") {
		t.Errorf("record warnings = %v, want the backend's own sentence", said)
	}
}
