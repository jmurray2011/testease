package judge_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

func TestIndependentRecordsCanReproduceDeclaredFacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionRecord(t, root, "first-20260902T120000Z", "sha256:scenario", "sha256:input", map[string]string{
		"evidence.sha256": "sha256:same", "target.version": "7.2.5",
	})
	two := reproductionRecord(t, root, "second-20260902T120001Z", "sha256:scenario", "sha256:input", map[string]string{
		"evidence.sha256": "sha256:same", "target.version": "7.2.5",
	})

	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatalf("ApplyReproduce: %v", err)
	}
	if verdict.Verdict != judge.Pass {
		t.Errorf("verdict = %q, want pass: %+v", verdict.Verdict, verdict.Steps)
	}
	if len(verdict.Evidence.Records) != 2 {
		t.Errorf("evidence = %+v, want both event streams pinned", verdict.Evidence)
	}
}

func TestAChangedStableFactIsARegression(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionRecord(t, root, "first-20260902T120000Z", "sha256:scenario", "sha256:input", map[string]string{
		"evidence.sha256": "sha256:first", "target.version": "7.2.5",
	})
	two := reproductionRecord(t, root, "second-20260902T120001Z", "sha256:scenario", "sha256:input", map[string]string{
		"evidence.sha256": "sha256:second", "target.version": "7.2.5",
	})

	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatalf("ApplyReproduce: %v", err)
	}
	if verdict.Verdict != judge.Regressed || verdict.Steps["evidence.sha256"].Verdict != judge.Unmet {
		t.Errorf("verdict = %+v, want changed evidence called a regression", verdict)
	}
}

func TestDifferentInputsMakeAComparisonUnverified(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionRecord(t, root, "first-20260902T120000Z", "sha256:scenario", "sha256:input-a", map[string]string{
		"evidence.sha256": "same", "target.version": "7.2.5",
	})
	two := reproductionRecord(t, root, "second-20260902T120001Z", "sha256:scenario", "sha256:input-b", map[string]string{
		"evidence.sha256": "same", "target.version": "7.2.5",
	})

	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatalf("ApplyReproduce: %v", err)
	}
	if verdict.Verdict != judge.Unverified || verdict.Steps["context"].Verdict != judge.Unverifiable {
		t.Errorf("verdict = %+v, want incompatible inputs unverified", verdict)
	}
}

func TestAComparisonVerdictIsStoredOutsideItsSourceRecords(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionRecord(t, root, "first-20260902T120000Z", "sha256:scenario", "sha256:input", map[string]string{
		"evidence.sha256": "same", "target.version": "7.2.5",
	})
	two := reproductionRecord(t, root, "second-20260902T120001Z", "sha256:scenario", "sha256:input", map[string]string{
		"evidence.sha256": "same", "target.version": "7.2.5",
	})
	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatal(err)
	}
	id, path, outcome, err := judge.SaveComparison(root, verdict)
	if err != nil {
		t.Fatalf("SaveComparison: %v", err)
	}
	if id == "" || outcome != judge.Written {
		t.Errorf("id=%q outcome=%v", id, outcome)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("comparison verdict was not written: %v", err)
	}
	for _, rec := range []*record.Record{one, two} {
		if _, err := os.Stat(filepath.Join(rec.Dir, judge.VerdictsDir, "reproduce@"+judge.ReproduceVersion+".json")); !os.IsNotExist(err) {
			t.Errorf("source record %s was given the cross-record verdict", rec.Dir)
		}
	}
}

func TestMatrixRecordsReproduceCorrespondingCells(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionMatrix(t, root, "first-20260902T120000Z", map[string]string{
		"6.2.4": "sha256:old", "7.2.2": "sha256:new",
	})
	two := reproductionMatrix(t, root, "second-20260902T120001Z", map[string]string{
		"6.2.4": "sha256:old", "7.2.2": "sha256:new",
	})

	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatalf("ApplyReproduce: %v", err)
	}
	if verdict.Verdict != judge.Pass {
		t.Errorf("verdict = %q, want pass: %+v", verdict.Verdict, verdict.Steps)
	}
	if verdict.Steps["6.2.4/evidence.sha256"].Verdict != judge.Met || verdict.Steps["7.2.2/evidence.sha256"].Verdict != judge.Met {
		t.Errorf("corresponding cells were not compared independently: %+v", verdict.Steps)
	}
	if len(verdict.Evidence.Records) != 6 {
		t.Errorf("evidence = %+v, want two parents and four child event streams pinned", verdict.Evidence)
	}
}

func TestAChangedStableFactRegressesOnlyItsMatrixCell(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionMatrix(t, root, "first-20260902T120000Z", map[string]string{
		"6.2.4": "sha256:old", "7.2.2": "sha256:new",
	})
	two := reproductionMatrix(t, root, "second-20260902T120001Z", map[string]string{
		"6.2.4": "sha256:changed", "7.2.2": "sha256:new",
	})

	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatalf("ApplyReproduce: %v", err)
	}
	if verdict.Verdict != judge.Regressed || verdict.Steps["6.2.4/evidence.sha256"].Verdict != judge.Unmet {
		t.Errorf("verdict = %+v, want the changed cell called a regression", verdict)
	}
	if verdict.Steps["7.2.2/evidence.sha256"].Verdict != judge.Met {
		t.Errorf("unchanged cell = %+v, want met", verdict.Steps["7.2.2/evidence.sha256"])
	}
}

func TestChangedMatrixScopeIsUnverified(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	one := reproductionMatrix(t, root, "first-20260902T120000Z", map[string]string{
		"6.2.4": "sha256:old", "7.2.2": "sha256:new",
	})
	two := reproductionMatrix(t, root, "second-20260902T120001Z", map[string]string{
		"6.2.4": "sha256:old",
	})

	verdict, err := judge.ApplyReproduce([]*record.Record{one, two})
	if err != nil {
		t.Fatalf("ApplyReproduce: %v", err)
	}
	if verdict.Verdict != judge.Unverified || verdict.Steps["context"].Verdict != judge.Unverifiable {
		t.Errorf("verdict = %+v, want changed matrix scope unverified", verdict)
	}
}

func reproductionMatrix(t *testing.T, root, id string, evidenceByCell map[string]string) *record.Record {
	t.Helper()
	dir := filepath.Join(root, id)
	w, err := record.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	stable := []string{"evidence.sha256", "target.version"}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID: id, Scenario: record.ScenarioRef{Name: "security", Hash: "sha256:scenario"},
		Reproduce: &record.ReproduceRef{Facts: stable},
	}); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(evidenceByCell))
	for key := range evidenceByCell {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: key}); err != nil {
			t.Fatal(err)
		}
		cellFacts := map[string]string{"evidence.sha256": evidenceByCell[key], "target.version": "7.2.5"}
		_ = reproductionRecord(t, filepath.Join(dir, record.CellsDir), id, "sha256:scenario", "sha256:input", cellFacts)
		cellDir := filepath.Join(dir, record.CellsDir, id)
		wantDir := filepath.Join(dir, record.CellsDir, key)
		if err := os.Rename(cellDir, wantDir); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: key, Status: record.StatusMeasured}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func reproductionRecord(t *testing.T, root, id, scenarioHash, inputHash string, facts map[string]string) *record.Record {
	t.Helper()
	dir := filepath.Join(root, id)
	w, err := record.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	stable := []string{"evidence.sha256", "target.version"}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID: id, Scenario: record.ScenarioRef{Name: "security", Hash: scenarioHash},
		Reproduce: &record.ReproduceRef{Facts: stable},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Emit(record.KindInputResolved, 0, record.InputResolved{Name: "suite", SHA256: inputHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Emit(record.KindStepStarted, 1, record.StepStarted{Label: "collect", Host: "app"}); err != nil {
		t.Fatal(err)
	}
	for key, value := range facts {
		v := value
		if _, err := w.Emit(record.KindFact, 1, record.Fact{Key: key, Value: &v}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Emit(record.KindOperation, 1, record.Operation{RC: 0, Status: record.StatusMeasured}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}
