package judge_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/judge"
	"github.com/jmurray2011/testease/internal/record"
)

// THE CENSUS GATE, judge side. A sweep's verdict is the thing a finding cites,
// and every case here kills a mutation that no test before it could see.

// A cell nobody measured has not passed. Manufacturing a verdict for it is the
// impersonation the whole design exists to prevent: the judge would be saying
// something about a product no probe read.
func TestACellThatDidNotMeasureIsUnjudgedNotPassed(t *testing.T) {
	t.Parallel()

	rec := judgeableCensus(
		t,
		judged{key: "5.0.2", status: record.StatusMeasured, met: true},
		judged{key: "6.0.3", status: record.StatusInfraFail},
		judged{key: "7.1.3", status: record.StatusSkipped},
	)

	result, err := judge.Matrix{}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	for _, key := range []string{"6.0.3", "7.1.3"} {
		if _, claimed := result.Cells[key]; claimed {
			t.Errorf("cell %s reached a verdict on evidence nobody took: %v", key, result.Cells[key])
		}
		if _, said := result.Unjudged[key]; !said {
			t.Errorf("cell %s was neither judged nor named unjudged", key)
		}
	}
	if len(result.Cells) != 1 {
		t.Errorf("cells judged = %d, want only the one that measured", len(result.Cells))
	}
}

// One cell that regressed makes the census regressed. A rollup that dropped it
// would report pass over a sweep with a failure in it.
func TestOneRegressedCellCarriesTheCensus(t *testing.T) {
	t.Parallel()

	rec := judgeableCensus(
		t,
		judged{key: "5.0.2", status: record.StatusMeasured, met: true},
		judged{key: "6.0.3", status: record.StatusMeasured, met: false},
	)

	result, err := judge.Matrix{}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if result.Verdict == judge.Pass {
		t.Errorf("verdict = %s over a cell that did not meet its expectation: %v", result.Verdict, result.Cells)
	}
}

// A verdict over many records has to say which bytes it read. Without the
// per-cell hash, a census conclusion cannot be checked against the cells it
// came from, and re-judging it later proves nothing.
func TestACensusVerdictPinsTheCellsItRead(t *testing.T) {
	t.Parallel()

	rec := judgeableCensus(
		t,
		judged{key: "5.0.2", status: record.StatusMeasured, met: true},
		judged{key: "6.0.3", status: record.StatusMeasured, met: true},
	)

	result, err := judge.Matrix{}.Judge(rec)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	for _, key := range []string{"5.0.2", "6.0.3"} {
		if result.CellEvidence[key] == "" {
			t.Errorf("cell %s was judged and its bytes were not pinned: %v", key, result.CellEvidence)
		}
	}
}

// judged is one cell of a census: what became of it, and whether the
// expectation its step declared was met.
type judged struct {
	key    string
	status record.Status
	met    bool
}

func judgeableCensus(t *testing.T, cells ...judged) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("sweep", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, c := range cells {
		if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: c.key}); err != nil {
			t.Fatalf("cell-started: %v", err)
		}
		if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: c.key, Status: c.status}); err != nil {
			t.Fatalf("cell-finished: %v", err)
		}
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, c := range cells {
		if c.status != record.StatusMeasured {
			// A cell that never measured leaves no record of its own to read,
			// which is the shape the parent is describing.
			continue
		}
		rc := 0
		if !c.met {
			rc = 1
		}
		body := `{"schema":1,"seq":1,"kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":2,"kind":"step-started","step":1,"data":{"label":"install","host":"box"}}
{"schema":1,"seq":3,"kind":"control-checked","step":1,"data":{"label":"install","expected":{"rc":"0"},"observed":{"rc":"0"},"ok":true}}
{"schema":1,"seq":4,"kind":"operation","step":1,"data":{"rc":` + itoa(rc) + `,"duration_s":1,"claim":{"text":""},"status":"measured"}}
{"schema":1,"seq":5,"kind":"run-finished","step":0,"data":{"status":"measured"}}
`
		cellDir := filepath.Join(dir, record.CellsDir, c.key)
		if err := os.MkdirAll(cellDir, 0o755); err != nil {
			t.Fatalf("make cell: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cellDir, record.EventsFile), []byte(body), 0o644); err != nil {
			t.Fatalf("write cell: %v", err)
		}
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}
