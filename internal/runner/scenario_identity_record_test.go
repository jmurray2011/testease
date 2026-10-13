package runner_test

import (
	"testing"

	"github.com/jmurray2011/testease/internal/record"
)

func TestRunStartedRecordsRuntimeAndCompleteTreeIdentity(t *testing.T) {
	t.Parallel()

	_, rec, err := exec(t, plan(t, oneStep), &fakeSession{run: exits(0, "ok\n")}, runOpts{})
	if err != nil {
		t.Fatal(err)
	}
	started := only[record.RunStarted](t, rec, record.KindRunStarted)
	if started.Scenario.HashVersion != "runtime-v2" {
		t.Errorf("hash version = %q", started.Scenario.HashVersion)
	}
	if started.Scenario.TreeHash == "" || len(started.Scenario.Files) == 0 {
		t.Errorf("complete tree identity missing: %+v", started.Scenario)
	}
}
