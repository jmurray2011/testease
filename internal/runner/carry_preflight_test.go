package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

func TestACarrySourceWithoutASessionRefusesBeforeAnyStepRuns(t *testing.T) {
	t.Parallel()

	source := &shotTarget{}
	destination := &receivingShotTarget{shotTarget: &shotTarget{}}
	providers := map[string]target.Provider{
		"source-provider":      fixedProvider{target: source},
		"destination-provider": fixedProvider{target: destination},
	}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan: plan(t, carryScenario), RunsDir: t.TempDir(),
		Tool: record.ToolRef{Version: "0.0.0-test"}, Providers: providers,
	})
	if err == nil {
		t.Fatal("the runner reached a carry whose source cannot fetch")
	}
	var precondition *runner.PreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("error is %T (%v), want *runner.PreconditionError", err, err)
	}
	if !strings.Contains(precondition.Error(), "source") || !strings.Contains(precondition.Error(), "session") {
		t.Errorf("precondition = %q, want source and session named", precondition)
	}
	if len(source.ran) != 0 {
		t.Fatalf("source ran %d earlier probe(s) before refusing the carry topology", len(source.ran))
	}

	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, event := range rec.Events {
		if event.Kind == record.KindOperation {
			t.Fatal("an operation was recorded before the inferred capability refusal")
		}
	}
}
