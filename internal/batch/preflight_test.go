package batch

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

type preflightInvoker struct {
	calls [][]string
}

func (i *preflightInvoker) Invoke(_ context.Context, stdout, stderr io.Writer, args ...string) int {
	i.calls = append(i.calls, slices.Clone(args))
	if !slices.Contains(args, "doctor") {
		_, _ = fmt.Fprintln(stderr, "run was started")
		return 10
	}
	if slices.Contains(args, "variant=missing") {
		_, _ = io.WriteString(stdout, `{"checks":[{"name":"inputs","status":"fail","found":"bundle is missing","remedy":"stage the RPM"}]}`)
		return 3
	}
	_, _ = io.WriteString(stdout, `{"checks":[{"name":"inputs","status":"ok","found":"bundle is ready"}]}`)
	return 0
}

func TestPreflightChecksEveryNamedEntryWithRunBindingsBeforeScheduling(t *testing.T) {
	definition := &Definition{Name: "variants", Tests: []Test{
		{Name: "missing", Scenario: "alpha", Params: map[string]string{"variant": "missing"}, Target: "vm-1", TargetCatalog: "/private/targets.yaml", InputBundle: "/private/inputs.yaml", AWSRegion: "us-east-1"},
		{Name: "ready", Scenario: "alpha", Params: map[string]string{"variant": "ready"}},
	}}
	invoker := &preflightInvoker{}
	got := Preflight(context.Background(), Options{Definition: definition, RunsDir: "/runs", ScenarioRoot: "/scenarios", Invoker: invoker}, true)
	if got.Status != "failed" || len(got.Tests) != 2 || got.Tests[0].Name != "missing" || got.Tests[0].Checks[0].Name != "inputs" || got.Tests[1].Status != "passed" {
		t.Fatalf("preflight = %+v", got)
	}
	if len(invoker.calls) != 2 {
		t.Fatalf("calls = %v, want a doctor call for every entry", invoker.calls)
	}
	for _, call := range invoker.calls {
		if !slices.Contains(call, "doctor") || !slices.Contains(call, "--strict") || !slices.Contains(call, "--output") || !slices.Contains(call, "json") {
			t.Errorf("not a strict JSON doctor call: %v", call)
		}
	}
	for _, want := range []string{"variant=missing", "vm-1", "/private/targets.yaml", "/private/inputs.yaml", "us-east-1"} {
		if !strings.Contains(strings.Join(invoker.calls[0], " "), want) {
			t.Errorf("first doctor call %v lacks %q", invoker.calls[0], want)
		}
	}
}
