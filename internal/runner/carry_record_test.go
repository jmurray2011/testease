package runner_test

import (
	"context"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

func TestACarryRequestIsExplicitInStepStarted(t *testing.T) {
	t.Parallel()

	providers := map[string]target.Provider{
		"source-provider":      &fakeProvider{target: &fakeTarget{session: &fakeSession{run: exits(0, "produced\n")}}},
		"destination-provider": &fakeProvider{target: &fakeTarget{session: &fakeSession{run: exits(0, "inspected\n")}}},
	}
	out, err := runner.Run(context.Background(), runner.Options{
		Plan: plan(t, carryScenario), RunsDir: t.TempDir(),
		Tool: record.ToolRef{Version: "0.0.0-test"}, Providers: providers,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec, err := record.Read(out.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range rec.Events {
		if event.Kind != record.KindStepStarted || event.Step != 2 {
			continue
		}
		started, decodeErr := record.Decode[record.StepStarted](event)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if started.Carry == nil || started.Carry.From != "/out/fixture.dump" || started.Carry.ToHost != "destination" || started.Carry.To != "/fixture.dump" {
			t.Fatalf("step-started carry = %+v, want the complete cross-host request", started.Carry)
		}
		return
	}
	t.Fatal("carry step-started event not found")
}
