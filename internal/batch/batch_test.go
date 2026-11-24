package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

type progressInvoker struct{}

func (progressInvoker) Invoke(_ context.Context, stdout, stderr io.Writer, args ...string) int {
	pos := slices.Index(args, "run")
	name := args[pos+1]
	_, _ = fmt.Fprintln(stderr, "probe started")
	_, _ = fmt.Fprintf(stdout, `{"run_id":"%s-run"}`, name)
	return 0
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestConcurrentChildProgressNamesItsScenario(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "batch.yaml")
	if err := os.WriteFile(path, []byte("schema: 1\nname: smoke\ntests:\n  - scenario: alpha\n  - scenario: bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var progress lockedBuffer
	_, err = Run(context.Background(), Options{Definition: definition, RunsDir: dir, ScenarioRoot: dir, Invoker: progressInvoker{}, Progress: &progress})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[alpha] probe started", "[bravo] probe started"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress %q lacks %q", progress.String(), want)
		}
	}
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
	// Stay live until a second child has been seen live at the same time,
	// with a bound. A fixed sleep let a loaded machine finish the first child
	// before starting the second, and read as a batch that never overlapped.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		i.mu.Lock()
		overlapped := i.most >= 2
		i.mu.Unlock()
		if overlapped {
			break
		}
	}
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

func TestBatchResolvesExplicitPathsRelativeToItsFileAndRecordsBindings(t *testing.T) {
	root := t.TempDir()
	batchDir := filepath.Join(root, "batches")
	if err := os.Mkdir(batchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(batchDir, "portable.yaml")
	definition := `schema: 1
name: portable
tests:
  - scenario: alpha
    path_params: {store: ../batch-media/package-a}
  - scenario: bravo
    input_bundle_path: ../batch-media/service-inputs.yaml
`
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "batch-media", "package-a")
	inputs := filepath.Join(root, "batch-media", "service-inputs.yaml")
	if got := loaded.Tests[0].Params["store"]; got != store {
		t.Fatalf("resolved store = %q, want %q", got, store)
	}
	if got := loaded.Tests[1].InputBundle; got != inputs {
		t.Fatalf("resolved input bundle = %q, want %q", got, inputs)
	}
	preflightInvoker := &preflightInvoker{}
	preflight := Preflight(context.Background(), Options{Definition: loaded, RunsDir: root, ScenarioRoot: root, Invoker: preflightInvoker}, true)
	if preflight.Status != "passed" || !slices.Contains(preflightInvoker.calls[0], "store="+store) || !slices.Contains(preflightInvoker.calls[1], inputs) {
		t.Fatalf("preflight omitted resolved paths: %+v, calls=%v", preflight, preflightInvoker.calls)
	}
	invoker := &recordingInvoker{args: map[string][]string{}}
	result, err := Run(context.Background(), Options{Definition: loaded, RunsDir: root, ScenarioRoot: root, Invoker: invoker})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(invoker.args["alpha"], "store="+store) || !slices.Contains(invoker.args["bravo"], inputs) {
		t.Fatalf("child arguments omit resolved paths: %v", invoker.args)
	}
	body, err := os.ReadFile(filepath.Join(result.Dir, "batch.json"))
	if err != nil {
		t.Fatal(err)
	}
	// As JSON writes them: a Windows path's backslashes are escaped there.
	if !strings.Contains(string(body), jsonString(t, store)) || !strings.Contains(string(body), jsonString(t, inputs)) {
		t.Fatalf("batch record omits resolved paths: %s", body)
	}
	snapshot, err := os.ReadFile(filepath.Join(result.Dir, "batch.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot) != definition {
		t.Fatalf("source snapshot changed: %s", snapshot)
	}
}

// jsonString is s as it appears inside a JSON string, without the quotes.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw[1 : len(raw)-1])
}

func TestBatchRejectsAmbiguousOrInvalidPathBindings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"duplicate parameter", "    params: {store: /old}\n    path_params: {store: ../media}\n", "both params and path_params"},
		{"duplicate input bundle", "    input_bundle: /old/inputs.yaml\n    input_bundle_path: ../inputs.yaml\n", "both input_bundle and input_bundle_path"},
		{"absolute path parameter", "    path_params: {store: /absolute/media}\n", "nonempty relative path"},
		{"empty path parameter", "    path_params: {store: ''}\n", "nonempty relative path"},
		{"absolute input bundle", "    input_bundle_path: /absolute/inputs.yaml\n", "nonempty relative path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "batch.yaml")
			body := "schema: 1\nname: smoke\ntests:\n  - scenario: alpha\n" + tc.body
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load error = %v, want %q", err, tc.want)
			}
		})
	}
}

type variantInvoker struct {
	mu      sync.Mutex
	active  int
	most    int
	calls   map[string][]string
	started chan struct{}
	release chan struct{}
}

func (i *variantInvoker) Invoke(_ context.Context, stdout, stderr io.Writer, args ...string) int {
	suffixAt := slices.Index(args, "--run-suffix")
	paramAt := slices.Index(args, "--param")
	if suffixAt < 0 || suffixAt+1 >= len(args) || paramAt < 0 || paramAt+1 >= len(args) {
		return 2
	}
	variant := args[paramAt+1]
	i.mu.Lock()
	i.active++
	i.most = max(i.most, i.active)
	i.calls[variant] = slices.Clone(args)
	i.mu.Unlock()
	_, _ = fmt.Fprintln(stderr, "probe started")
	i.started <- struct{}{}
	<-i.release
	_, _ = fmt.Fprintf(stdout, `{"run_id":"alpha-20261020T120000Z-%s"}`, args[suffixAt+1])
	i.mu.Lock()
	i.active--
	i.mu.Unlock()
	return 0
}

func TestNamedEntriesCanRunTheSameScenarioConcurrentlyWithDistinctRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "batch.yaml")
	definition := `schema: 1
name: two-variants
parallel: 2
tests:
  - name: writer
    scenario: alpha
    params: {history: writer}
    target: writer-vm
    target_catalog: /private/targets.yaml
  - name: clean
    scenario: alpha
    params: {history: clean}
    target: clean-vm
    target_catalog: /private/targets.yaml
`
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load named variants: %v", err)
	}
	invoker := &variantInvoker{calls: map[string][]string{}, started: make(chan struct{}, 2), release: make(chan struct{})}
	var progress lockedBuffer
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := Run(context.Background(), Options{
			Definition: loaded, RunsDir: dir, ScenarioRoot: "/scenarios", Invoker: invoker,
			Progress: &progress, Now: func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 123, time.UTC) },
		})
		done <- outcome{result, err}
	}()
	for range 2 {
		select {
		case <-invoker.started:
		case <-time.After(2 * time.Second):
			close(invoker.release)
			<-done
			t.Fatal("both named variants did not start together")
		}
	}
	close(invoker.release)
	finished := <-done
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	result := finished.result
	if invoker.most != 2 || len(invoker.calls) != 2 {
		t.Fatalf("maximum live = %d, calls = %v; want both variants live", invoker.most, invoker.calls)
	}
	for variant, target := range map[string]string{"history=writer": "writer-vm", "history=clean": "clean-vm"} {
		if !slices.Contains(invoker.calls[variant], target) {
			t.Errorf("%s invocation = %v, want target %s", variant, invoker.calls[variant], target)
		}
	}
	if result.Tests[0].RunID == result.Tests[1].RunID || result.Tests[0].Log == result.Tests[1].Log {
		t.Fatalf("variants shared a run id or log: %+v", result.Tests)
	}
	if result.Tests[0].Log != "writer.log" || result.Tests[1].Log != "clean.log" {
		t.Fatalf("logs = %q, %q, want entry names", result.Tests[0].Log, result.Tests[1].Log)
	}
	for _, want := range []string{"[writer] probe started", "[clean] probe started"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress %q lacks %q", progress.String(), want)
		}
	}
	body, err := os.ReadFile(filepath.Join(result.Dir, "batch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Tests []struct {
			Name     string `json:"name"`
			Scenario string `json:"scenario"`
			RunID    string `json:"run_id"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Tests) != 2 || saved.Tests[0].Name != "writer" || saved.Tests[1].Name != "clean" || saved.Tests[0].Scenario != "alpha" || saved.Tests[1].Scenario != "alpha" || saved.Tests[0].RunID == saved.Tests[1].RunID {
		t.Fatalf("saved entries = %+v", saved.Tests)
	}
}

func TestBatchRejectsDuplicateOrUnsafeEntryNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tests string
		want  string
	}{
		{"same explicit name", "  - {name: same, scenario: alpha}\n  - {name: same, scenario: bravo}\n", "duplicate test name"},
		{"explicit name collides with default", "  - {scenario: alpha}\n  - {name: alpha, scenario: bravo}\n", "duplicate test name"},
		{"same unnamed scenario", "  - {scenario: alpha}\n  - {scenario: alpha}\n", "duplicate test name"},
		{"unsafe path", "  - {name: ../escape, scenario: alpha}\n", "invalid test name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "batch.yaml")
			if err := os.WriteFile(path, []byte("schema: 1\nname: smoke\ntests:\n"+tc.tests), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load error = %v, want %q", err, tc.want)
			}
		})
	}
}

type heldInvoker struct {
	started chan string
	release map[string]chan struct{}
}

func (i heldInvoker) Invoke(_ context.Context, stdout, _ io.Writer, args ...string) int {
	name := args[slices.Index(args, "run")+1]
	i.started <- name
	<-i.release[name]
	_, _ = fmt.Fprintf(stdout, `{"run_id":"%s-run"}`, name)
	return 0
}

func TestBatchWaitsForStartedChildrenWhenSavingTheNextStatusFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "batch.yaml")
	if err := os.WriteFile(path, []byte("schema: 1\nname: smoke\nparallel: 2\ntests:\n  - scenario: alpha\n  - scenario: bravo\n  - scenario: charlie\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	batchDir := filepath.Join(dir, "batches", "smoke-20261020T120000.000000000Z")
	invoker := heldInvoker{started: make(chan string, 2), release: map[string]chan struct{}{
		"alpha": make(chan struct{}), "bravo": make(chan struct{}), "charlie": make(chan struct{}),
	}}
	done := make(chan error, 1)
	go func() {
		_, runErr := Run(context.Background(), Options{Definition: definition, RunsDir: dir, ScenarioRoot: dir, Invoker: invoker, Now: func() time.Time { return now }})
		done <- runErr
	}()
	for range 2 {
		<-invoker.started
	}
	// The next save fails: batch.json is now a directory it cannot be renamed
	// over. Removing the whole batch directory would do the same on unix, but
	// Windows refuses to delete the logs the running children hold open.
	record := filepath.Join(batchDir, "batch.json")
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(record, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	close(invoker.release["alpha"])
	select {
	case err := <-done:
		t.Fatalf("batch returned before bravo stopped: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(invoker.release["bravo"])
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost batch record did not cause an error")
		}
	case <-time.After(time.Second):
		t.Fatal("batch did not stop after both children ended")
	}
}
