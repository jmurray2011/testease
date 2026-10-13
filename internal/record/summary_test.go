package record_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
)

// run.json is a cacheable summary and never the record. After a hard process
// death the events are complete up to the crash and this is absent or stale,
// which is why every consumer reads events and may use this only as a cache it
// can rebuild.

func summarised(t *testing.T, write func(w *record.Writer)) (string, *record.Record) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("summed", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	write(w)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return dir, rec
}

func TestASummaryIsRebuiltFromTheEvents(t *testing.T) {
	t.Parallel()

	dir, rec := summarised(t, func(w *record.Writer) {
		if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
			RunID:    "summed-20260812T140322Z",
			Scenario: record.ScenarioRef{Name: "summed", Hash: "sha256:abc"},
			Tool:     record.ToolRef{Version: "0.1.0"},
			Driver:   record.DriverManifest,
			Params:   map[string]string{"from": "1.0"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Emit(record.KindTargetResolved, 0, record.TargetResolved{
			Host:      "box",
			Requested: map[string]any{"image": "neutral:1"},
			Resolved:  map[string]any{"ref": "container:abc"},
			Facts:     map[string]string{"os": "neutral"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Emit(record.KindStepStarted, 1, record.StepStarted{Label: "do it", Host: "box"}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Emit(record.KindOperation, 1, record.Operation{
			RC: 0, DurationS: 3, Status: record.StatusMeasured,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{
			Status: record.StatusMeasured, DurationS: 4.5,
		}); err != nil {
			t.Fatal(err)
		}
	})

	s := record.Summarize(rec)
	if s.RunID != "summed-20260812T140322Z" || s.Scenario.Name != "summed" {
		t.Errorf("summary = %+v", s)
	}
	if s.Tool.Version != "0.1.0" || s.Driver != record.DriverManifest {
		t.Errorf("provenance thin: %+v", s)
	}
	if s.Params["from"] != "1.0" {
		t.Errorf("params = %v", s.Params)
	}
	if s.Status != record.StatusMeasured || s.Steps[record.StatusMeasured] != 1 {
		t.Errorf("rollup = %q %v", s.Status, s.Steps)
	}
	if len(s.Targets) != 1 || s.Targets[0].Host != "box" {
		t.Errorf("targets = %+v, want the identity layers kept", s.Targets)
	}
	if s.DurationS != 4.5 {
		t.Errorf("duration = %v", s.DurationS)
	}
	_ = dir
}

func TestASummaryPinsTheEventsItWasBuiltFrom(t *testing.T) {
	t.Parallel()

	// A stale or absent hash is how a reader detects a stale summary, which is
	// the whole reason this is a cache rather than a second source of truth.
	dir, rec := summarised(t, func(w *record.Writer) {
		if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
			t.Fatal(err)
		}
	})

	s := record.Summarize(rec)
	if !strings.HasPrefix(s.EventsHash, "sha256:") {
		t.Errorf("events_hash = %q", s.EventsHash)
	}

	body, err := os.ReadFile(filepath.Join(dir, record.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if s.EventsHash != record.HashBytes(body) {
		t.Error("the summary does not pin the events it was built from")
	}
}

func TestASummaryNeverCarriesAVerdict(t *testing.T) {
	t.Parallel()

	// Resolved, deliberately: a verdict in the summary is a second semantic
	// artifact that can disagree with the verdict file, and the record must be
	// self-contained as evidence rather than as a conclusion.
	_, rec := summarised(t, func(w *record.Writer) {
		if _, err := w.Emit(record.KindControlChecked, 1, record.ControlChecked{
			Label: "CONTROL negative", OK: false,
		}); err != nil {
			t.Fatal(err)
		}
	})

	body, err := record.EncodeSummary(record.Summarize(rec))
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"verdict", "regressed", "pass"} {
		if strings.Contains(strings.ToLower(string(body)), word) {
			t.Errorf("the summary mentions %q; a summary holds no conclusions", word)
		}
	}
}

func TestAnInterruptedRunSummarisesAsInterrupted(t *testing.T) {
	t.Parallel()

	_, rec := summarised(t, func(w *record.Writer) {
		if _, err := w.Emit(record.KindStepStarted, 1, record.StepStarted{Label: "cut short"}); err != nil {
			t.Fatal(err)
		}
	})

	s := record.Summarize(rec)
	if s.Status != record.StatusInterrupted {
		t.Errorf("status = %q, want %q: no run-finished is what interrupted means", s.Status, record.StatusInterrupted)
	}
}

func TestACensusSummarisesItsCells(t *testing.T) {
	t.Parallel()

	_, rec := summarised(t, func(w *record.Writer) {
		if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
			Matrix: &record.Matrix{Axes: map[string][]string{"from": {"1.0", "2.0"}}, Source: "cli"},
		}); err != nil {
			t.Fatal(err)
		}
		for key, status := range map[string]record.Status{"1.0": record.StatusMeasured, "2.0": record.StatusInfraFail} {
			if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: key, Status: status}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusInfraFail}); err != nil {
			t.Fatal(err)
		}
	})

	s := record.Summarize(rec)
	if s.Matrix == nil || len(s.Matrix.Axes["from"]) != 2 {
		t.Errorf("matrix = %+v, want the scope kept", s.Matrix)
	}
	if s.Cells[record.StatusMeasured] != 1 || s.Cells[record.StatusInfraFail] != 1 {
		t.Errorf("cells = %v", s.Cells)
	}
}

func TestWritingASummaryAndReadingItBack(t *testing.T) {
	t.Parallel()

	dir, rec := summarised(t, func(w *record.Writer) {
		if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
			t.Fatal(err)
		}
	})

	if err := record.WriteSummary(dir, record.Summarize(rec)); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, record.SummaryFile))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(body), "\n") {
		t.Error("run.json does not end in a newline")
	}

	// Regenerating it from the same events must produce the same bytes, or it
	// is not a cache.
	again, err := record.EncodeSummary(record.Summarize(rec))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(body) {
		t.Error("regenerating the summary produced different bytes")
	}
}

func TestOneOrderingOfTheAxis(t *testing.T) {
	t.Parallel()

	// Two orderings is how a record comes to say infra-fail about a run whose
	// exit code said probe-fail: both following a rule, neither agreeing.
	cases := []struct {
		a, b, want record.Status
	}{
		{record.StatusMeasured, record.StatusProbeFail, record.StatusProbeFail},
		{record.StatusInfraFail, record.StatusProbeFail, record.StatusProbeFail},
		{record.StatusProbeFail, record.StatusInfraFail, record.StatusProbeFail},
		{record.StatusInterrupted, record.StatusInfraFail, record.StatusInfraFail},
		{record.StatusMeasured, record.StatusSkipped, record.StatusSkipped},
		{record.StatusMeasured, record.StatusMeasured, record.StatusMeasured},
		{record.StatusUnfinished, record.StatusMeasured, record.StatusUnfinished},
	}
	for _, c := range cases {
		if got := record.Worse(c.a, c.b); got != c.want {
			t.Errorf("Worse(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestOneWordForAUnitThatNeverFinished(t *testing.T) {
	t.Parallel()

	// It goes into verdict files that findings cite, so two readers using two
	// words for one fact would put both into the corpus.
	if record.StatusUnfinished == "" {
		t.Error("there is no word for a unit that started and never reported")
	}
}
