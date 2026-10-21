package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

type recordingInvoker struct {
	mu      sync.Mutex
	active  int
	most    int
	started []string
	args    map[string][]string
}

func (i *recordingInvoker) Invoke(_ context.Context, stdout, _ io.Writer, args ...string) int {
	pos := slices.Index(args, "run")
	if pos < 0 || pos+1 >= len(args) {
		return 2
	}
	scenario := args[pos+1]
	i.mu.Lock()
	i.active++
	i.most = max(i.most, i.active)
	i.started = append(i.started, scenario)
	i.args[scenario] = slices.Clone(args)
	i.mu.Unlock()
	time.Sleep(25 * time.Millisecond)
	_, _ = fmt.Fprintf(stdout, `{"run_id":"%s-run"}`, scenario)
	i.mu.Lock()
	i.active--
	i.mu.Unlock()
	if scenario == "bravo" {
		return 11
	}
	return 0
}

func TestRunStartsIndependentScenariosTogetherAndRecordsEveryResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "batch.yaml")
	definition := `schema: 1
name: smoke
parallel: 2
tests:
  - scenario: alpha
    params: {version: one}
    target: alpha-target
    target_catalog: /private/alpha.yaml
  - scenario: bravo
  - scenario: charlie
`
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	invoker := &recordingInvoker{args: map[string][]string{}}
	result, err := Run(context.Background(), Options{
		Definition: loaded, RunsDir: dir, ScenarioRoot: "/scenarios", Invoker: invoker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invoker.most != 2 || len(invoker.started) != 3 {
		t.Fatalf("maximum live = %d, started = %v; want two live and all three started", invoker.most, invoker.started)
	}
	if result.ExitCode != 11 || len(result.Tests) != 3 || result.Tests[1].RunID != "bravo-run" || result.Tests[1].ExitCode != 11 {
		t.Fatalf("result = %+v", result)
	}
	if args := invoker.args["alpha"]; !slices.Contains(args, "version=one") || !slices.Contains(args, "alpha-target") || !slices.Contains(args, "/private/alpha.yaml") {
		t.Fatalf("alpha invocation = %v", args)
	}
	body, err := os.ReadFile(filepath.Join(result.Dir, "batch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Result
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Tests[2].RunID != "charlie-run" || saved.ExitCode != 11 {
		t.Fatalf("saved result = %+v", saved)
	}
}
