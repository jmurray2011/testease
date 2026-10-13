package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/scenario"
)

// THE EXAMPLE IS THE FIRST THING A NEWCOMER RUNS, so it is held to the same
// bar as the corpus: it loads, it resolves, its probe is there, and it
// declares an expectation that could fail. An example that rots is worse than
// none - it teaches the shape wrong to the one person with no way to tell.
func TestTheExampleScenarioStillLoads(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "examples")
	names, err := scenario.Names(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	if len(names) == 0 {
		t.Fatal("the examples tree is empty; the quickstart in README.md runs nothing")
	}

	for _, name := range names {
		m, err := scenario.Load(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		plan, err := m.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: resolve: %v", name, err)
		}
		if len(plan.Steps) == 0 {
			t.Errorf("%s declares no steps", name)
		}
		for _, step := range plan.Steps {
			// A step whose expectation cannot fail is a label, not a check -
			// which is the thing the example exists to demonstrate.
			if step.Expect == nil ||
				(step.Expect.RC == nil && step.Expect.Fingerprint == "" && len(step.Expect.Facts) == 0) {
				t.Errorf("%s: step %q declares nothing it could fail", name, step.Label)
			}
			if scenario.IsDrive(step.Probe) {
				continue
			}
			probe := filepath.Join(root, name, step.Probe)
			info, err := os.Stat(probe)
			if err != nil {
				t.Errorf("%s: step %q names %s, which is not there: %v", name, step.Label, step.Probe, err)
				continue
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("%s: %s is not executable", name, step.Probe)
			}
		}
	}
}

// The example's probe writes on the structured channel with the verbs the
// runner actually parses. A newcomer copies this file; `key=value` is silently
// ignored, and the step then reads as though the probe took no reading.
func TestTheExampleProbeUsesTheStructuredChannelsOwnVerbs(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", "examples", "hello", "probes", "whoami.sh"))
	if err != nil {
		t.Fatalf("read the example probe: %v", err)
	}
	text := string(body)

	if !strings.Contains(text, `'fact\t%s\t%s\n'`) {
		t.Error("the example does not emit a tab-separated fact; a reader copying it would record nothing")
	}
	if !strings.Contains(text, `'no-reading\t%s\t%s\n'`) {
		t.Error("the example never shows a reading it could not take, which is half the contract")
	}
	if !strings.Contains(text, ">&3") {
		t.Error("the example does not write on fd 3")
	}
}
