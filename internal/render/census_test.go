package render_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/render"
)

// A census is read through its parent, never through its cells. "Repairs are
// read-time and reported" is only true if the projection somebody actually
// reads reports them: a cell whose events.jsonl the reader had to repair says
// so when that cell is rendered directly, and a 136-cell sweep is never
// rendered that way.

func TestACensusReportsWhatItsCellsHadToTolerate(t *testing.T) {
	t.Parallel()

	rec := census(t, damagedCell)

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, nil); err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if !strings.Contains(notices.String(), "7.2.3") {
		t.Errorf("rows named no damaged cell: %q", notices.String())
	}
	if !strings.Contains(notices.String(), "not valid JSON") {
		t.Errorf("rows hid a cell's read repair: %q", notices.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(data.String()), "\n") {
		if strings.Contains(line, "not valid JSON") {
			t.Errorf("the notice leaked into the data: %q", line)
		}
	}
}

func TestACleanCensusIsNotCalledDamaged(t *testing.T) {
	t.Parallel()

	rec := census(t, cleanCell)

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, nil); err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if strings.Contains(notices.String(), "warning") {
		t.Errorf("a clean census was reported as damaged: %q", notices.String())
	}
}

// The page and the json carry the same notices as the terminal. A census read
// through -o html is the one a finding links to.
func TestEveryEncodingOfACensusCarriesItsCellsRepairs(t *testing.T) {
	t.Parallel()

	rec := census(t, damagedCell)

	doc, err := render.ProjectFiltered(rec, "rows", nil)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if !strings.Contains(strings.Join(doc.Notices, "\n"), "not valid JSON") {
		t.Errorf("the document hid a cell's read repair: %v", doc.Notices)
	}
}

// census writes a parent that scheduled one cell, and the cell underneath it.
func census(t *testing.T, cell func(*testing.T, string)) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("sweep", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: filepath.Base(dir)}); err != nil {
		t.Fatalf("run-started: %v", err)
	}
	if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: "7.2.3"}); err != nil {
		t.Fatalf("cell-started: %v", err)
	}
	if _, err := w.Emit(record.KindCellFinished, 0, record.CellFinished{Key: "7.2.3", Status: record.StatusMeasured}); err != nil {
		t.Fatalf("cell-finished: %v", err)
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cell(t, filepath.Join(dir, record.CellsDir, "7.2.3"))

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

// damagedCell is a cell that measured and whose file carries a line no reader
// can parse - the shape a truncated write or a interleaved writer leaves.
func damagedCell(t *testing.T, dir string) {
	t.Helper()
	writeCell(t, dir, cleanLines+"\n{\"schema\":1,\"seq\":3,\"ts\":\"2026-08-12T14:03:24.000Z\",\"kind\":\"fact\",\"data\":{\n")
}

func cleanCell(t *testing.T, dir string) {
	t.Helper()
	writeCell(t, dir, cleanLines+"\n")
}

const cleanLines = `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:23.000Z","kind":"fact","step":1,"data":{"key":"app.version","value":"7.2.3"}}`

func writeCell(t *testing.T, dir, lines string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make cell: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, record.EventsFile), []byte(lines), 0o644); err != nil {
		t.Fatalf("write cell events: %v", err)
	}
}

// The facts view reads a record's own facts. A census keeps its facts in its
// cells, so a census rendered through it prints a header and no rows - which
// reads exactly like a census that measured nothing.
func TestTheFactsViewSaysWhyACensusLooksEmpty(t *testing.T) {
	t.Parallel()

	rec := census(t, cleanCell)

	var data, notices bytes.Buffer
	if err := render.FactsOf(&data, &notices, []*record.Record{rec}, nil); err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if !strings.Contains(notices.String(), "census") {
		t.Errorf("facts rendered a census as an empty table with no explanation: %q", notices.String())
	}
	if !strings.Contains(notices.String(), "rows") {
		t.Errorf("facts named no view that would answer: %q", notices.String())
	}
	if !strings.Contains(notices.String(), filepath.Base(rec.Dir)) {
		t.Errorf("the notice named no record: %q", notices.String())
	}
}

func TestTheFactsViewDoesNotCallASingleRunACensus(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), record.RunID("one", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	value := "7.2.3"
	if _, err := w.Emit(record.KindFact, 1, record.Fact{Key: "app.version", Value: &value}); err != nil {
		t.Fatalf("fact: %v", err)
	}
	if _, err := w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured}); err != nil {
		t.Fatalf("run-finished: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var data, notices bytes.Buffer
	if err := render.FactsOf(&data, &notices, []*record.Record{rec}, nil); err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if strings.Contains(notices.String(), "census") {
		t.Errorf("a single run was called a census: %q", notices.String())
	}
}

func TestTheFactsPageSaysItTooWhenACensusLooksEmpty(t *testing.T) {
	t.Parallel()

	rec := census(t, cleanCell)

	doc, err := render.ProjectFactsOf([]*record.Record{rec}, nil)
	if err != nil {
		t.Fatalf("ProjectFacts: %v", err)
	}
	if !strings.Contains(strings.Join(doc.Notices, "\n"), "census") {
		t.Errorf("the document explained nothing about an empty census: %v", doc.Notices)
	}
}

// THE CENSUS GATE. Every case below kills one mutation that the tests before
// it did not: the census path carried no coverage of status fidelity, of
// absence, of which attempt is read, or of the flattening the step views have
// been guarding since check-record-row.sh. A sweep is what the corpus is read
// through, and a wrong cell here is a wrong reading of a whole rung.

// A cell that finished infra-fail is not a cell that measured. Overwriting the
// status with `measured` turns a sweep that half-failed into a clean one.
func TestACellRendersTheStatusItFinishedWith(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("app.version", "5.0.2")},
		cell{key: "6.0.3", status: record.StatusInfraFail, body: factLines("app.version", "6.0.3")},
		cell{key: "7.1.3", status: record.StatusProbeFail, body: factLines("app.version", "7.1.3")},
	)

	for key, want := range map[string]string{
		"5.0.2": string(record.StatusMeasured),
		"6.0.3": string(record.StatusInfraFail),
		"7.1.3": string(record.StatusProbeFail),
	} {
		if got := cellField(t, rec, key, "status"); got != want {
			t.Errorf("cell %s status = %q, want %q", key, got, want)
		}
	}
}

// A cell that did not measure still gets a row. Dropping it leaves a census
// that reads as though it only ever contained the cells that worked.
func TestACellThatDidNotMeasureIsStillACell(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("app.version", "5.0.2")},
		cell{key: "6.0.3", status: record.StatusSkipped, body: ""},
		cell{key: "7.1.3", status: record.StatusUnfinished, body: ""},
	)

	rows := cellLines(t, rec)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: a census that stopped is not a census that never covered them", len(rows))
	}
}

// A reading nobody took is not zero. This is the same rule the step views
// spend a dash on, and a count-shaped fact is exactly where a zero would be
// believed.
func TestACellThatRecordedNoFactRendersAbsentNotZero(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("documents", "27")},
		cell{key: "6.0.3", status: record.StatusMeasured, body: factLines("users", "3")},
	)

	// Read off the RENDERED TEXT. The document leaves an absent key out of the
	// row entirely and each encoding puts the marker back, so asking the
	// document cannot see a view that fills the gap with something else.
	if got := textField(t, rec, "6.0.3", "documents"); got != "-" {
		t.Errorf("a fact 6.0.3 never recorded rendered %q, want the absent marker", got)
	}
	if got := textField(t, rec, "5.0.2", "documents"); got != "27" {
		t.Errorf("documents = %q, want the reading", got)
	}
}

// A fact only a later cell recorded is still a column. Collecting names from
// the first cell alone makes a dimension nobody else measured disappear.
func TestAFactOnlyOneCellRecordedIsStillAColumn(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("users", "3")},
		cell{key: "6.0.3", status: record.StatusMeasured, body: factLines("schema.version", "131")},
	)

	if got := cellField(t, rec, "6.0.3", "schema.version"); got != "131" {
		t.Errorf("schema.version = %q, want the reading only the later cell took", got)
	}
}

// A tab in a fact value must not become a column boundary. This is the gate's
// oldest lesson - a row that is not the shape it claims produces a WRONG
// reading from a correct measurement - and the census never had it.
func TestAFactValueThatFightsBackDoesNotBreakTheRow(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		// A REAL tab and a REAL newline: written as escapes in the JSON, they
		// arrive in the value as the bytes that break a table.
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("claim", "one\ttwo\nthree")},
	)

	rows := cellLines(t, rec)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: a newline in a value became a row", len(rows))
	}
	if strings.ContainsAny(rows[0], "\t\n") {
		t.Errorf("row = %q, want the separators flattened out of the value", rows[0])
	}
	got := cellField(t, rec, "5.0.2", "claim")
	if strings.ContainsAny(got, "\t\n") {
		t.Errorf("claim = %q, want the separators flattened out of it", got)
	}
	if got == "-" || got == "" {
		t.Errorf("claim = %q, want the reading kept rather than dropped", got)
	}
}

// A resumed cell's earlier attempt is still in its file, as evidence. It is
// not part of the story the census tells: a key only the failed attempt
// recorded must not surface beside the re-measured ones.
func TestAResumedCellRendersTheAttemptThatStands(t *testing.T) {
	t.Parallel()

	rec := censusOf(t, cell{key: "7.1.3", status: record.StatusMeasured, body: `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:23.000Z","kind":"fact","step":1,"data":{"key":"app.version","value":"7.1.2"}}
{"schema":1,"seq":3,"ts":"2026-08-12T14:03:24.000Z","kind":"fact","step":1,"data":{"key":"first.attempt.only","value":"yes"}}
{"schema":1,"seq":4,"ts":"2026-08-12T14:05:00.000Z","kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":5,"ts":"2026-08-12T14:05:01.000Z","kind":"fact","step":1,"data":{"key":"app.version","value":"7.1.3"}}
`})

	if got := cellField(t, rec, "7.1.3", "app.version"); got != "7.1.3" {
		t.Errorf("app.version = %q, want the attempt that re-measured", got)
	}
	if got := cellField(t, rec, "7.1.3", "first.attempt.only"); got != "-" {
		t.Errorf("first.attempt.only = %q, want it absent: it belongs to an attempt that was superseded", got)
	}
}

// The page and the json are the same projection in another encoding, not a
// second opinion. A census whose rows view has cells must not have an empty
// one here.
func TestTheCensusDocumentCarriesTheSameCellsAsTheTerminal(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("users", "3")},
		cell{key: "6.0.3", status: record.StatusInfraFail, body: ""},
	)

	doc, err := render.ProjectFiltered(rec, "rows", nil)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(doc.Rows) != 2 {
		t.Fatalf("document rows = %d, want 2", len(doc.Rows))
	}
	if !slices.Contains(doc.Columns, "users") {
		t.Errorf("columns = %v, want the fact a cell recorded", doc.Columns)
	}
	for _, row := range doc.Rows {
		if row["cell"] == "6.0.3" && row["status"] != string(record.StatusInfraFail) {
			t.Errorf("6.0.3 status = %q, want %s", row["status"], record.StatusInfraFail)
		}
	}
}

// Every damaged cell is reported, not the first one. A sweep with three torn
// cells that names one leaves two believed clean.
func TestEveryDamagedCellIsReportedNotTheFirst(t *testing.T) {
	t.Parallel()

	torn := cleanLines + "\n{\"schema\":1,\"seq\":3,\"kind\":\"fact\",\"data\":{\n"
	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: torn},
		cell{key: "6.0.3", status: record.StatusMeasured, body: torn},
		cell{key: "7.1.3", status: record.StatusMeasured, body: torn},
	)

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, nil); err != nil {
		t.Fatalf("Rows: %v", err)
	}
	for _, key := range []string{"5.0.2", "6.0.3", "7.1.3"} {
		if !strings.Contains(notices.String(), key) {
			t.Errorf("cell %s went unreported: %q", key, notices.String())
		}
	}
}

// cell is one cell of a census: what the parent said became of it, and what
// its own file holds. The body is written verbatim so a case can carry damage,
// a resumed attempt, or a value that fights back.
type cell struct {
	key    string
	status record.Status
	body   string
}

// censusOf writes a parent that scheduled these cells, and each cell under it.
func censusOf(t *testing.T, cells ...cell) *record.Record {
	t.Helper()

	dir := filepath.Join(t.TempDir(), record.RunID("sweep", time.Now()))
	w, err := record.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: filepath.Base(dir)}); err != nil {
		t.Fatalf("run-started: %v", err)
	}
	for _, c := range cells {
		if _, err := w.Emit(record.KindCellStarted, 0, record.CellStarted{Key: c.key}); err != nil {
			t.Fatalf("cell-started: %v", err)
		}
	}
	for _, c := range cells {
		if c.status == record.StatusUnfinished {
			// A cell that started and never finished leaves no cell-finished,
			// and not finishing is itself the answer.
			continue
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
		if c.body == "" {
			continue
		}
		writeCell(t, filepath.Join(dir, record.CellsDir, c.key), c.body)
	}

	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec
}

// factLines is a minimal cell that measured and recorded one reading.
func factLines(key, value string) string {
	return fmt.Sprintf(`{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:23.000Z","kind":"fact","step":1,"data":{"key":%q,"value":%q}}
`, key, value)
}

// cellLines is the rendered rows, without the header.
func cellLines(t *testing.T, rec *record.Record) []string {
	t.Helper()

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, nil); err != nil {
		t.Fatalf("Rows: %v", err)
	}
	lines := strings.Split(strings.TrimRight(data.String(), "\n"), "\n")
	if len(lines) < 1 {
		t.Fatalf("Rows wrote nothing")
	}
	return lines[1:]
}

// cellField reads one column of one cell out of the document, which is the
// projection the text view lays out - so a case asserts a value rather than a
// column position in a tabwriter's padding.
func cellField(t *testing.T, rec *record.Record, key, column string) string {
	t.Helper()

	doc, err := render.ProjectFiltered(rec, "rows", nil)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	for _, row := range doc.Rows {
		if row["cell"] != key {
			continue
		}
		if value, ok := row[column]; ok {
			return value
		}
		// The document leaves an absent key out; every encoding puts the
		// marker back, and a case asking about absence wants the marker.
		return "-"
	}
	t.Fatalf("no cell %q in %v", key, doc.Rows)
	return ""
}

// The facts view reads the attempt that stands too. A ladder rendered across
// records is read rung by rung, and a superseded attempt's reading appearing
// in a column would be attributed to the run that heads it.
func TestTheFactsViewReadsTheAttemptThatStands(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), record.RunID("rung", time.Now()))
	writeCell(t, dir, `{"schema":1,"seq":1,"ts":"2026-08-12T14:03:22.184Z","kind":"run-started","step":0,"data":{"run_id":"rung"}}
{"schema":1,"seq":2,"ts":"2026-08-12T14:03:23.000Z","kind":"fact","step":1,"data":{"key":"app.version","value":"7.1.2"}}
{"schema":1,"seq":3,"ts":"2026-08-12T14:03:24.000Z","kind":"fact","step":1,"data":{"key":"first.attempt.only","value":"yes"}}
{"schema":1,"seq":4,"ts":"2026-08-12T14:05:00.000Z","kind":"run-started","step":0,"data":{"run_id":"rung"}}
{"schema":1,"seq":5,"ts":"2026-08-12T14:05:01.000Z","kind":"fact","step":1,"data":{"key":"app.version","value":"7.1.3"}}
`)
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	doc, err := render.ProjectFactsOf([]*record.Record{rec}, nil)
	if err != nil {
		t.Fatalf("ProjectFacts: %v", err)
	}
	id := filepath.Base(dir)
	for _, row := range doc.Rows {
		switch row["fact"] {
		case "app.version":
			if row[id] != "7.1.3" {
				t.Errorf("app.version = %q, want the attempt that re-measured", row[id])
			}
		case "first.attempt.only":
			t.Errorf("a key only a superseded attempt recorded became a row: %v", row)
		}
	}
}

// textField reads one column of one cell out of the RENDERED TEXT. The
// document and the text differ in exactly one place - absence - so a case
// about what a reader sees in a gap has to look here.
func textField(t *testing.T, rec *record.Record, key, column string) string {
	t.Helper()

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, nil); err != nil {
		t.Fatalf("Rows: %v", err)
	}
	lines := strings.Split(strings.TrimRight(data.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("Rows wrote no cells: %q", data.String())
	}
	gap := regexp.MustCompile(`  +`)
	header := gap.Split(strings.TrimRight(lines[0], " "), -1)
	at := slices.Index(header, column)
	if at < 0 {
		t.Fatalf("no column %q in %v", column, header)
	}
	for _, line := range lines[1:] {
		fields := gap.Split(strings.TrimRight(line, " "), -1)
		if len(fields) == 0 || fields[0] != key {
			continue
		}
		if at >= len(fields) {
			t.Fatalf("cell %s has %d fields, want column %d (%s): %q", key, len(fields), at, column, line)
		}
		return fields[at]
	}
	t.Fatalf("no cell %q in %q", key, data.String())
	return ""
}

// A 136-cell sweep records dozens of keys per cell, and the table is then
// wider than anything can read. Narrowing it is selection, not derivation:
// every value shown is still one a probe recorded.
func TestACensusCanBeNarrowedToTheReadingsTheQuestionIsAbout(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: `{"schema":1,"seq":1,"kind":"run-started","step":0,"data":{"run_id":"cell"}}
{"schema":1,"seq":2,"kind":"fact","step":1,"data":{"key":"app.version","value":"5.0.2"}}
{"schema":1,"seq":3,"kind":"fact","step":1,"data":{"key":"noise","value":"loud"}}
`},
	)

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, []string{"app.version"}); err != nil {
		t.Fatalf("RowsOf: %v", err)
	}
	if !strings.Contains(data.String(), "app.version") {
		t.Errorf("the reading asked for is missing: %q", data.String())
	}
	if strings.Contains(data.String(), "noise") {
		t.Errorf("a reading nobody asked for was rendered: %q", data.String())
	}
	if !strings.Contains(data.String(), "5.0.2") {
		t.Errorf("the value is missing: %q", data.String())
	}
}

// A key nothing recorded is still a column, rendered absent. Dropping it would
// answer a narrower question than the one asked, and the table would look as
// though the key had never been sought.
func TestAKeyNothingRecordedIsStillTheColumnItWasAskedFor(t *testing.T) {
	t.Parallel()

	rec := censusOf(
		t,
		cell{key: "5.0.2", status: record.StatusMeasured, body: factLines("app.version", "5.0.2")},
	)

	var data, notices bytes.Buffer
	if err := render.RowsOf(&data, &notices, rec, []string{"app.version", "never.measured"}); err != nil {
		t.Fatalf("RowsOf: %v", err)
	}
	if !strings.Contains(data.String(), "never.measured") {
		t.Errorf("a key asked for was dropped rather than rendered absent: %q", data.String())
	}
	if !strings.Contains(strings.Split(data.String(), "\n")[1], "-") {
		t.Errorf("row = %q, want the absent marker under the key nothing recorded", data.String())
	}
}

// The same narrowing across records, where a ladder has one row per key.
func TestTheFactsViewCanBeNarrowedToo(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), record.RunID("rung", time.Now()))
	writeCell(t, dir, `{"schema":1,"seq":1,"kind":"run-started","step":0,"data":{"run_id":"rung"}}
{"schema":1,"seq":2,"kind":"fact","step":1,"data":{"key":"app.version","value":"7.1.3"}}
{"schema":1,"seq":3,"kind":"fact","step":1,"data":{"key":"noise","value":"loud"}}
`)
	rec, err := record.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	doc, err := render.ProjectFactsOf([]*record.Record{rec}, []string{"app.version"})
	if err != nil {
		t.Fatalf("ProjectFactsOf: %v", err)
	}
	if len(doc.Rows) != 1 {
		t.Fatalf("rows = %d, want the one key asked for: %v", len(doc.Rows), doc.Rows)
	}
	if doc.Rows[0]["fact"] != "app.version" {
		t.Errorf("row = %v, want app.version", doc.Rows[0])
	}
}
