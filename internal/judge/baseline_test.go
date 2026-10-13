package judge_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// The baseline judge is the only one whose answer depends on something outside
// the record it is given. That extra input is pinned in the verdict, and moving
// it is somebody else's decision to act on - never a side effect of judging.

// recordWithReport writes a record whose drive step produced a report.
func recordWithReport(t *testing.T, body string, facts map[string]string) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("app", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID:    filepath.Base(dir),
		Scenario: record.ScenarioRef{Name: "app"},
		Params:   map[string]string{"version": "3.0"},
	}); err != nil {
		t.Fatalf("run-started: %v", err)
	}
	for key, value := range facts {
		v := value
		if _, err := w.Emit(record.KindFact, 1, record.Fact{Key: key, Value: &v}); err != nil {
			t.Fatalf("fact: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", "report.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

// sameOrDiffer is a comparator in the shape the judge asks for: exit 0 when the
// two files match, non-zero when they do not.
func sameOrDiffer(t *testing.T) []string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "compare.sh")
	body := "#!/bin/bash\nif cmp -s \"$1\" \"$2\"; then exit 0; fi\necho 'the report differs from the baseline'\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write comparator: %v", err)
	}
	return []string{path}
}

func TestAFirstRunEstablishesTheBaseline(t *testing.T) {
	t.Parallel()

	// Nothing to compare against. Calling that a pass would claim a comparison
	// nobody made.
	rec := recordWithReport(t, `{"tests":1}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	b := judge.Baseline{Store: judge.DirStore{Dir: t.TempDir()}, Compare: sameOrDiffer(t)}

	v, err := judge.Apply(b, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v.Verdict != judge.Established {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Established)
	}
}

func TestJudgingNeverAdvancesTheBaseline(t *testing.T) {
	t.Parallel()

	// The whole reason the judge only decides: a judge that promoted as it
	// judged would compare the next run's report against itself and reach a
	// different answer for the same evidence.
	store := judge.DirStore{Dir: t.TempDir()}
	rec := recordWithReport(t, `{"tests":1}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	b := judge.Baseline{Store: store, Compare: sameOrDiffer(t)}

	if _, err := judge.Apply(b, rec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, found, err := store.Get(b.Key(rec)); err != nil || found {
		t.Errorf("judging wrote a baseline (found=%v, err=%v)", found, err)
	}
}

func TestACleanRunSaysPromoteAndARegressionSaysHold(t *testing.T) {
	t.Parallel()

	store := judge.DirStore{Dir: t.TempDir()}
	compare := sameOrDiffer(t)

	rec := recordWithReport(t, `{"tests":1}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	b := judge.Baseline{Store: store, Compare: compare}
	if err := store.Promote(b.Key(rec), strings.NewReader(`{"tests":1}`)); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	clean, err := judge.Apply(b, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if clean.Verdict != judge.Promote {
		t.Errorf("verdict = %q, want %q", clean.Verdict, judge.Promote)
	}

	moved := recordWithReport(t, `{"tests":2}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	regressed, err := judge.Apply(judge.Baseline{Store: store, Compare: compare}, moved)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if regressed.Verdict != judge.Hold {
		t.Errorf("verdict = %q, want %q", regressed.Verdict, judge.Hold)
	}
	if because := strings.Join(regressed.Steps[b.Key(moved)].Because, "; "); !strings.Contains(because, "differs") {
		t.Errorf("because = %q, want the comparator's own words", because)
	}
}

func TestTheJudgeSaysTheSameThingHoweverTheCallerFeelsAboutIt(t *testing.T) {
	t.Parallel()

	// A judge whose answer changed because somebody passed a flag would not be
	// a function of the record: the same run, judged twice by the same version,
	// would reach two verdicts. Accepting a regression anyway is an act, and it
	// happens on the other side of the judging.
	store := judge.DirStore{Dir: t.TempDir()}
	rec := recordWithReport(t, `{"tests":2}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	b := judge.Baseline{Store: store, Compare: sameOrDiffer(t)}
	if err := store.Promote(b.Key(rec), strings.NewReader(`{"tests":1}`)); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	v, err := judge.Apply(b, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if v.Verdict != judge.Hold {
		t.Errorf("verdict = %q, want %q", v.Verdict, judge.Hold)
	}
}

func TestTheVerdictPinsTheBaselineItRead(t *testing.T) {
	t.Parallel()

	store := judge.DirStore{Dir: t.TempDir()}
	rec := recordWithReport(t, `{"tests":1}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	b := judge.Baseline{Store: store, Compare: sameOrDiffer(t)}
	if err := store.Promote(b.Key(rec), strings.NewReader(`{"tests":1}`)); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	v, err := judge.Apply(b, rec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// A verdict whose answer depended on something outside the record has to
	// say which something, or nobody can check it later.
	if hash := v.Evidence.Extra["baseline"]; !strings.HasPrefix(hash, "sha256:") {
		t.Errorf("evidence = %+v, want the baseline pinned", v.Evidence)
	}
}

func TestTheKeyIsWhatTheScenarioSaidItWas(t *testing.T) {
	t.Parallel()

	named := recordWithReport(t, `{}`, map[string]string{
		judge.ReportFact:      "artifacts/report.json",
		judge.BaselineKeyFact: "docs-7.2.3",
	})
	b := judge.Baseline{Store: judge.DirStore{Dir: t.TempDir()}, Compare: sameOrDiffer(t)}
	if got := b.Key(named); got != "docs-7.2.3" {
		t.Errorf("key = %q, want the scenario's own", got)
	}

	// Without one, the key is the scenario and what it ran with - the same
	// statement, made less explicitly.
	derived := recordWithReport(t, `{}`, map[string]string{judge.ReportFact: "artifacts/report.json"})
	if got := b.Key(derived); got != "app-version-3.0" {
		t.Errorf("key = %q, want it derived from the scenario and its params", got)
	}
}

func TestARecordWithNoReportIsNotJudged(t *testing.T) {
	t.Parallel()

	rec := recordWithReport(t, "", nil)
	b := judge.Baseline{Store: judge.DirStore{Dir: t.TempDir()}, Compare: sameOrDiffer(t)}

	_, err := judge.Apply(b, rec)
	if err == nil {
		t.Fatal("a run with no report was judged against a baseline")
	}
	var nothing *judge.NothingToJudgeError
	if !errors.As(err, &nothing) {
		t.Fatalf("error is %T, want *judge.NothingToJudgeError", err)
	}
}

func TestARecordNamingAReportThatIsNotThere(t *testing.T) {
	t.Parallel()

	rec := recordWithReport(t, "", map[string]string{judge.ReportFact: "artifacts/report.json"})
	b := judge.Baseline{Store: judge.DirStore{Dir: t.TempDir()}, Compare: sameOrDiffer(t)}

	if _, err := judge.Apply(b, rec); err == nil {
		t.Fatal("a missing report was compared anyway")
	}
}

func TestAComparatorThatCannotRunIsAnErrorNotAHold(t *testing.T) {
	t.Parallel()

	// A comparator that never started made no comparison, and Hold is a
	// verdict against the product. Worse, judge.Save would persist that Hold,
	// so re-judging with the comparator fixed would trip DisagreesError and
	// accuse the judge of impurity.
	store := judge.DirStore{Dir: t.TempDir()}
	if err := store.Promote("app-version-3.0", strings.NewReader("report")); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
	rec := recordWithReport(t, "report", map[string]string{record.ReportFact: "artifacts/report.json"})

	b := judge.Baseline{Store: store, Compare: []string{filepath.Join(t.TempDir(), "no-such-comparator")}}
	if _, err := b.Judge(rec); err == nil {
		t.Fatal("a comparator that never ran produced a verdict")
	}
}

func TestAnUnconfiguredBaselineJudgeSaysWhatItNeeds(t *testing.T) {
	t.Parallel()

	rec := recordWithReport(t, `{}`, map[string]string{judge.ReportFact: "artifacts/report.json"})

	_, err := judge.Apply(judge.Baseline{}, rec)
	if err == nil {
		t.Fatal("an unconfigured baseline judge judged something")
	}
	var precondition *judge.PreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("error is %T, want *judge.PreconditionError", err)
	}
	if precondition.Remedy == "" {
		t.Error("the refusal names no way to fix it")
	}
}

func TestTheStoreKeepsWhatItIsGiven(t *testing.T) {
	t.Parallel()

	store := judge.DirStore{Dir: filepath.Join(t.TempDir(), "baselines")}
	if err := store.Promote("docs/7.2.3", strings.NewReader("the report")); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	body, found, err := store.Get("docs/7.2.3")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	defer func() { _ = body.Close() }()

	got, err := os.ReadFile(store.Path("docs/7.2.3"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "the report" {
		t.Errorf("baseline = %q", got)
	}
	// A key names a product and a version, and has to survive being a filename.
	if strings.Contains(filepath.Base(store.Path("docs/7.2.3")), "/") {
		t.Error("a key with a slash in it became a path")
	}
}
