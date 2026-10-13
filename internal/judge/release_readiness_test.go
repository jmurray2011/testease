package judge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

func TestReleaseReadinessNamesProductOutcomes(t *testing.T) {
	t.Parallel()

	manager := readinessFacts()
	manager = replaceFacts(
		manager,
		fact("restart.unit_present", "no"),
		fact("final.launcher_present", "no"),
		fact("final.svc", "unknown"),
		fact("final.http8080", "404"),
		fact("final.http443", "404"),
		fact("final.schema", "3.0.5"),
		fact("diagnostic.legacy_jvm", "yes"),
		fact("diagnostic.target_jvm", "no"),
	)
	application := readinessFacts()
	application = replaceFacts(
		application,
		fact("final.http8080", "404"),
		fact("final.http443", "404"),
		fact("final.schema", "5.1.1"),
		fact("diagnostic.quartz_locks_missing", "yes"),
	)
	payload := readinessFacts()
	payload = replaceFacts(payload, fact("sbom.file_delta_changed_count", "1"))
	upgrade := readinessFacts()
	upgrade = replaceFacts(
		upgrade,
		fact("retry.rc", "1"),
		fact("retry.refused", "yes"),
		fact("final.pkg", "3.0.5"),
		fact("sbom.installed_version", "3.0.5"),
		fact("sbom.target_installed", "no"),
	)

	rec := readinessCensus(
		t,
		readinessCell{key: "ready", facts: readinessFacts()},
		readinessCell{key: "manager", facts: manager},
		readinessCell{key: "application", facts: application},
		readinessCell{key: "payload", facts: payload},
		readinessCell{key: "upgrade", facts: upgrade},
	)
	result, err := (judge.ReleaseReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	want := map[string]string{
		"ready":       judge.ReleaseReady,
		"manager":     judge.ManagerTransitionFailed,
		"application": judge.ApplicationFailed,
		"payload":     judge.PayloadDrift,
		"upgrade":     judge.UpgradeFailed,
	}
	for key, verdict := range want {
		if got := result.Cells[key].Verdict; got != verdict {
			t.Errorf("%s = %q, want %q (%v)", key, got, verdict, result.Cells[key].Because)
		}
		if result.CellEvidence[key] == "" {
			t.Errorf("%s has no pinned evidence hash", key)
		}
	}
	if result.Verdict != judge.ReleaseBlocked {
		t.Errorf("census verdict = %q, want %q", result.Verdict, judge.ReleaseBlocked)
	}
	if because := strings.Join(result.Cells["application"].Because, "; "); !strings.Contains(because, "HTTP") || !strings.Contains(because, "schema") || !strings.Contains(because, "Quartz") {
		t.Errorf("application reasons = %q, want HTTP, schema and Quartz evidence", because)
	}
}

func TestReleaseReadinessRequiresPremiseAndCompleteReadings(t *testing.T) {
	t.Parallel()

	badPremise := readinessFacts()
	badPremise = replaceFacts(badPremise, fact("pre.ready", "no"))
	missing := readinessFacts()
	missing = removeFact(missing, "final.schema")
	rec := readinessCensus(
		t,
		readinessCell{key: "bad-premise", facts: badPremise},
		readinessCell{key: "missing", facts: missing},
	)
	result, err := (judge.ReleaseReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	for _, key := range []string{"bad-premise", "missing"} {
		if got := result.Cells[key].Verdict; got != judge.Unverified {
			t.Errorf("%s = %q, want %q", key, got, judge.Unverified)
		}
	}
	if result.Verdict != judge.Unverified {
		t.Errorf("census verdict = %q, want %q", result.Verdict, judge.Unverified)
	}
}

func TestEveryReadyCellMakesTheReleaseReady(t *testing.T) {
	t.Parallel()

	legacyFacts := readinessFacts()
	for _, key := range []string{
		"restart.unit_present",
		"final.launcher_present",
		"diagnostic.legacy_jvm",
		"diagnostic.target_jvm",
		"diagnostic.quartz_locks_missing",
	} {
		legacyFacts = removeFact(legacyFacts, key)
	}
	rec := readinessCensus(
		t,
		readinessCell{key: "6.2.4", facts: readinessFacts()},
		// Records made before manager diagnostics were added remain judgeable:
		// those readings refine a failure, but do not manufacture readiness.
		readinessCell{key: "7.2.1", facts: legacyFacts},
	)
	result, err := (judge.ReleaseReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if result.Verdict != judge.ReleaseReady {
		t.Errorf("verdict = %q, want %q: %+v", result.Verdict, judge.ReleaseReady, result.Cells)
	}
}

func TestBrowserAcceptanceIsAProductGateWhenTheScenarioRequiresIt(t *testing.T) {
	t.Parallel()

	ready := append(readinessFacts(), browserReadinessFacts()...)
	failed := replaceFacts(
		append(readinessFacts(), browserReadinessFacts()...),
		fact("pw.rc", "1"),
		fact("suite.status", "failed"),
		fact("suite.runbooks.failed", "1"),
		fact("suite.steps.failed", "1"),
	)
	missing := removeFact(append(readinessFacts(), browserReadinessFacts()...), "suite.status")
	rec := readinessCensus(
		t,
		readinessCell{key: "ready", facts: ready},
		readinessCell{key: "failed", facts: failed},
		readinessCell{key: "missing", facts: missing},
	)
	result, err := (judge.ReleaseReadiness{}).Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got := result.Cells["ready"].Verdict; got != judge.ReleaseReady {
		t.Errorf("ready = %q, want %q: %v", got, judge.ReleaseReady, result.Cells["ready"].Because)
	}
	if got := result.Cells["failed"].Verdict; got != judge.ApplicationFailed {
		t.Errorf("failed = %q, want %q: %v", got, judge.ApplicationFailed, result.Cells["failed"].Because)
	}
	if got := result.Cells["missing"].Verdict; got != judge.Unverified {
		t.Errorf("missing = %q, want %q: %v", got, judge.Unverified, result.Cells["missing"].Because)
	}
	if because := strings.Join(result.Cells["failed"].Because, "; "); !strings.Contains(because, "browser") {
		t.Errorf("browser failure reason = %q", because)
	}
}

type readinessCell struct {
	key    string
	status record.Status
	facts  []record.Fact
}

func readinessCensus(t *testing.T, cells ...readinessCell) *record.Record {
	t.Helper()
	dir := filepath.Join(t.TempDir(), record.RunID("upgrade", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open parent: %v", err)
	}
	for _, cell := range cells {
		status := cell.status
		if status == "" {
			status = record.StatusMeasured
		}
		if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: cell.key}); err != nil {
			t.Fatalf("cell-started: %v", err)
		}
		if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: cell.key, Status: status}); err != nil {
			t.Fatalf("cell-finished: %v", err)
		}
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close parent: %v", err)
	}

	for _, cell := range cells {
		if cell.status != "" && cell.status != record.StatusMeasured {
			continue
		}
		cellDir, _ := written(t, step{
			label:  "measure the upgrade",
			facts:  cell.facts,
			expect: map[string]string{"fact.pre.ready": "yes"},
		})
		destination := filepath.Join(dir, record.CellsDir, cell.key)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatalf("make cells dir: %v", err)
		}
		if err := os.Rename(cellDir, destination); err != nil {
			t.Fatalf("move cell: %v", err)
		}
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read parent: %v", err)
	}
	return rec
}

func readinessFacts() []record.Fact {
	return []record.Fact{
		fact("pre.ready", "yes"),
		fact("retry.rc", "0"),
		fact("retry.refused", "no"),
		fact("restart.unit_present", "yes"),
		fact("final.pkg", "7.2.3"),
		fact("final.launcher_present", "yes"),
		fact("final.svc", "active"),
		fact("final.http8080", "200"),
		fact("final.http443", "200"),
		fact("final.schema", "7.2.3.3"),
		fact("diagnostic.legacy_jvm", "no"),
		fact("diagnostic.target_jvm", "yes"),
		fact("diagnostic.quartz_locks_missing", "no"),
		fact("sbom.requested_version", "7.2.3"),
		fact("sbom.installed_version", "7.2.3"),
		fact("sbom.target_installed", "yes"),
		fact("sbom.component_delta_added_count", "0"),
		fact("sbom.component_delta_changed_count", "0"),
		fact("sbom.component_delta_missing_count", "0"),
		fact("sbom.file_delta_added_count", "0"),
		fact("sbom.file_delta_changed_count", "0"),
		fact("sbom.file_delta_missing_count", "0"),
		fact("sbom.unowned_code_file_count", "0"),
	}
}

func browserReadinessFacts() []record.Fact {
	return []record.Fact{
		fact("suite.acceptance", "required"),
		fact("suite.status", "passed"),
		fact("suite.app.version", "7.2.3"),
		fact("suite.runbooks", "1"),
		fact("suite.runbooks.failed", "0"),
		fact("suite.steps.failed", "0"),
		fact("pw.rc", "0"),
	}
}

func replaceFacts(facts []record.Fact, replacements ...record.Fact) []record.Fact {
	out := append([]record.Fact(nil), facts...)
	for _, replacement := range replacements {
		for i := range out {
			if out[i].Key == replacement.Key {
				out[i] = replacement
				break
			}
		}
	}
	return out
}

func removeFact(facts []record.Fact, key string) []record.Fact {
	out := make([]record.Fact, 0, len(facts)-1)
	for _, fact := range facts {
		if fact.Key != key {
			out = append(out, fact)
		}
	}
	return out
}
