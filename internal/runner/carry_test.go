package runner_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/runner"
	"github.com/jmurray2011/testease/internal/target"
)

type fixedProvider struct{ target target.Target }

func (p fixedProvider) Resolve(context.Context, target.Spec) (target.Target, error) {
	return p.target, nil
}
func (p fixedProvider) Release(context.Context, target.Target, bool) error { return nil }

type receivingShotTarget struct {
	*shotTarget
	receivedAt   string
	receivedBody []byte
}

func (t *receivingShotTarget) ReceiveCarry(_ context.Context, body io.Reader, remote string) (target.CarryReceipt, error) {
	t.receivedAt = remote
	t.receivedBody, _ = io.ReadAll(body)
	return target.CarryReceipt{SeededBeforeStart: true}, nil
}

const carryScenario = `
schema: 1
name: cross-host-carry
question: do exact bytes travel between two hosts?
hosts:
  source: {provider: source-provider, image: neutral-linux:1}
  destination: {provider: destination-provider, image: neutral-other:1}
steps:
  - label: produce the artifact
    host: source
    probe: probes/one.sh
  - label: carry the artifact
    host: source
    carry:
      from: /out/fixture.dump
      to_host: destination
      to: /fixture.dump
    expect: {rc: 0}
  - label: inspect the artifact
    host: destination
    probe: probes/two.sh
`

func TestACarryStepMovesExactBytesAndRecordsTheirIdentity(t *testing.T) {
	t.Parallel()

	source := &fakeSession{run: exits(0, "produced\n")}
	destination := &fakeSession{run: exits(0, "inspected\n")}
	providers := map[string]target.Provider{
		"source-provider":      &fakeProvider{target: &fakeTarget{session: source}},
		"destination-provider": &fakeProvider{target: &fakeTarget{session: destination}},
	}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan: plan(t, carryScenario), RunsDir: t.TempDir(),
		Tool: record.ToolRef{Version: "0.0.0-test"}, Providers: providers,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []byte("artifact body for /out/fixture.dump")
	if destination.putAt != "/fixture.dump" || string(destination.putBody) != string(want) {
		t.Fatalf("destination received %q at %q, want exact bytes at /fixture.dump", destination.putBody, destination.putAt)
	}

	rec, err := record.Read(out.Dir)
	if err != nil {
		t.Fatal(err)
	}
	wantFacts := map[string]string{
		"carry.source_host":      "source",
		"carry.source_path":      "/out/fixture.dump",
		"carry.destination_host": "destination",
		"carry.destination_path": "/fixture.dump",
		"carry.bytes":            fmt.Sprint(len(want)),
		"carry.sha256":           fmt.Sprintf("sha256:%x", sha256.Sum256(want)),
	}
	for _, event := range rec.Events {
		if event.Kind != record.KindFact || event.Step != 2 {
			continue
		}
		fact, decodeErr := record.Decode[record.Fact](event)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if wantValue, wanted := wantFacts[fact.Key]; wanted && fact.Value != nil {
			if *fact.Value != wantValue {
				t.Errorf("fact %s = %q, want %q", fact.Key, *fact.Value, wantValue)
			}
			delete(wantFacts, fact.Key)
		}
	}
	if len(wantFacts) != 0 {
		t.Errorf("carry facts missing: %v", wantFacts)
	}
}

func TestACarryCanSeedAOneShotDestinationBeforeItStarts(t *testing.T) {
	t.Parallel()

	source := &fakeSession{run: exits(0, "produced\n")}
	destination := &receivingShotTarget{shotTarget: &shotTarget{}}
	providers := map[string]target.Provider{
		"source-provider":      fixedProvider{target: &fakeTarget{session: source}},
		"destination-provider": fixedProvider{target: destination},
	}

	out, err := runner.Run(context.Background(), runner.Options{
		Plan: plan(t, carryScenario), RunsDir: t.TempDir(),
		Tool: record.ToolRef{Version: "0.0.0-test"}, Providers: providers,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if destination.receivedAt != "/fixture.dump" || string(destination.receivedBody) != "artifact body for /out/fixture.dump" {
		t.Fatalf("one-shot seed = %q at %q", destination.receivedBody, destination.receivedAt)
	}
	if len(destination.ran) != 1 {
		t.Fatalf("one-shot runs = %d, want the destination inspection after seeding", len(destination.ran))
	}

	rec, readErr := record.Read(out.Dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, ok := factIn(t, rec, "carry.seeded_before_start"); !ok || got != "yes" {
		t.Errorf("carry.seeded_before_start = %q, %v; want yes", got, ok)
	}
}
