package record_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
)

// Importing an old run.tsv is how a record taken before testease existed
// becomes something a judge can read. What comes out is evidence with a
// shorter provenance, and it says so: driver adhoc, because the structure was
// vouched for by a shell rather than by a manifest runner.

const oldRun = "seq\thost\tstep\texit\tseconds\tdrift\tclaim\n" +
	"1\thot\tinstall old rung\t0\t41\tno\tInstalled.\n" +
	"2\thot\tattempt upgrade\t1\t12\tyes\tUpgrade completed successfully!\n" +
	"3\thot\tCONTROL negative\t0\t2\tno\tRead nothing.\n"

func imported(t *testing.T, tsv string) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "aborted-upgrade-20260808T023158Z")
	if err := record.Import(dir, strings.NewReader(tsv), record.ImportOptions{
		RunID:    "aborted-upgrade-20260808T023158Z",
		Scenario: "aborted-upgrade",
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

func TestAnImportedRunSaysWhoVouchedForIt(t *testing.T) {
	t.Parallel()

	rec := imported(t, oldRun)
	started, err := record.Decode[record.RunStarted](firstOf(t, rec, record.KindRunStarted))
	if err != nil {
		t.Fatal(err)
	}

	// The marked escape hatch. A reader and a judge may treat it with a longer
	// spoon; it is still evidence.
	if started.Driver != record.DriverAdhoc {
		t.Errorf("driver = %q, want %q", started.Driver, record.DriverAdhoc)
	}
	if started.RunID != "aborted-upgrade-20260808T023158Z" {
		t.Errorf("run id = %q, want the one it was published under", started.RunID)
	}
}

func TestEveryRowBecomesAStepAndAnOperation(t *testing.T) {
	t.Parallel()

	rec := imported(t, oldRun)

	var labels []string
	var rcs []int
	for _, e := range rec.Events {
		switch e.Kind {
		case record.KindStepStarted:
			s, err := record.Decode[record.StepStarted](e)
			if err != nil {
				t.Fatal(err)
			}
			labels = append(labels, s.Label)
		case record.KindOperation:
			op, err := record.Decode[record.Operation](e)
			if err != nil {
				t.Fatal(err)
			}
			rcs = append(rcs, op.RC)
		}
	}
	if strings.Join(labels, "|") != "install old rung|attempt upgrade|CONTROL negative" {
		t.Errorf("labels = %v", labels)
	}
	if len(rcs) != 3 || rcs[1] != 1 {
		t.Errorf("rcs = %v", rcs)
	}
}

func TestTheDriftColumnBecomesAFingerprintAgain(t *testing.T) {
	t.Parallel()

	rec := imported(t, oldRun)

	var results []record.FingerprintResult
	for _, e := range rec.Events {
		if e.Kind != record.KindFingerprint {
			continue
		}
		fp, err := record.Decode[record.Fingerprint](e)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, fp.Result)
	}
	want := []record.FingerprintResult{
		record.FingerprintUnchanged, record.FingerprintChanged, record.FingerprintUnchanged,
	}
	if len(results) != 3 {
		t.Fatalf("fingerprints = %v", results)
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("fingerprint %d = %q, want %q", i, results[i], want[i])
		}
	}
}

func TestAnUnreadableDriftStaysUnknown(t *testing.T) {
	t.Parallel()

	// The old `?` meant the instrument said something nobody recognised, and
	// the raw value was already lost by then. Importing it as unchanged would
	// be inventing a reading; unknown is what is true.
	rec := imported(t, "seq\thost\tstep\texit\tseconds\tdrift\tclaim\n1\thot\ts\t0\t1\t?\tx\n")

	fp, err := record.Decode[record.Fingerprint](firstOf(t, rec, record.KindFingerprint))
	if err != nil {
		t.Fatal(err)
	}
	if fp.Result != record.FingerprintUnknown {
		t.Errorf("result = %q, want %q", fp.Result, record.FingerprintUnknown)
	}
}

func TestAMinusOneStaysMinusOne(t *testing.T) {
	t.Parallel()

	// -1 in an old row means the value would not parse when it was written.
	// Importing it as anything else would repair a defect the record made a
	// point of keeping.
	rec := imported(t, "seq\thost\tstep\texit\tseconds\tdrift\tclaim\n1\thot\ts\t-1\t-1\tno\tx\n")

	op, err := record.Decode[record.Operation](firstOf(t, rec, record.KindOperation))
	if err != nil {
		t.Fatal(err)
	}
	if op.RC != -1 || op.DurationS != -1 {
		t.Errorf("operation = %+v, want the -1s kept", op)
	}
}

func TestTheClaimSurvivesWithItsCarriageReturns(t *testing.T) {
	t.Parallel()

	rec := imported(t, "seq\thost\tstep\texit\tseconds\tdrift\tclaim\n1\thot\ts\t0\t1\tno\tdone\r\n")

	op, err := record.Decode[record.Operation](firstOf(t, rec, record.KindOperation))
	if err != nil {
		t.Fatal(err)
	}
	if op.Claim.Text != "done\r" {
		t.Errorf("claim = %q, want the CR kept", op.Claim.Text)
	}
}

func TestARowThatIsNotSevenFieldsIsKeptAndReported(t *testing.T) {
	t.Parallel()

	// The corpus this reads was written before the seven-field guard existed,
	// so some of it is malformed. A step that happened must still leave a
	// trace, and what could not be read has to be visible rather than dropped.
	rec := imported(t, "seq\thost\tstep\texit\tseconds\tdrift\tclaim\n1\thot\tbroken\n")

	var warned bool
	for _, e := range rec.Events {
		if e.Kind == record.KindRecordWarning {
			warned = true
		}
	}
	if !warned {
		t.Error("a malformed row was imported without a word")
	}
	if len(rec.Events) < 2 {
		t.Error("a malformed row left no trace at all")
	}
}

func TestTheHeaderIsNotAStep(t *testing.T) {
	t.Parallel()

	rec := imported(t, oldRun)
	for _, e := range rec.Events {
		if e.Kind != record.KindStepStarted {
			continue
		}
		s, err := record.Decode[record.StepStarted](e)
		if err != nil {
			t.Fatal(err)
		}
		if s.Label == "step" {
			t.Error("the header was imported as a step")
		}
	}
}

func firstOf(t *testing.T, rec *record.Record, kind record.Kind) record.Event {
	t.Helper()

	for _, e := range rec.Events {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %s event", kind)
	return record.Event{}
}
