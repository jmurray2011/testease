package render_test

import (
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/render"
)

// The page counts what it renders. It said six columns over a seven-column
// table, because it was counting the value columns and calling them the whole.
func TestPageCountsEveryColumnItRenders(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "tsv",
		Columns: []string{"seq", "host", "step"},
		Rows:    []map[string]string{{"seq": "1", "host": "alpha", "step": "one"}},
	})

	if !strings.Contains(out, "3 columns") {
		t.Fatalf("page does not count the label column:\n%s", headOf(out))
	}
}

// GROUPS COME FROM THE LABEL, NEVER FROM A VALUE. A fact key is dotted by this
// tool's own convention, so the part before the first dot is a name the record
// already carries - grouping on it reads the key, and a reader who collapses a
// group hides rows by what they are called rather than by what they said.
func TestPageGroupsRowsByTheirLabelPrefix(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a"},
		Rows: []map[string]string{
			{"fact": "alpha.one", "run-a": "1"},
			{"fact": "alpha.two", "run-a": "2"},
			{"fact": "beta.one", "run-a": "3"},
			{"fact": "beta.two", "run-a": "4"},
		},
	})

	for _, want := range []string{">alpha<", ">beta<"} {
		if !strings.Contains(out, want) {
			t.Fatalf("page has no %s group:\n%s", want, out)
		}
	}
	if got := strings.Count(out, `class="group"`); got != 2 {
		t.Fatalf("page has %d groups, want 2", got)
	}
}

// Grouping that puts every row in its own group is a heading per row, which is
// noise. Labels a view does not dot - a step number, a census cell - fall here.
func TestPageDoesNotGroupWhenEveryLabelIsItsOwnPrefix(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "rows",
		Columns: []string{"cell", "status"},
		Rows: []map[string]string{
			{"cell": "one", "status": "measured"},
			{"cell": "two", "status": "measured"},
		},
	})

	if strings.Contains(out, `class="group"`) {
		t.Fatalf("page grouped rows that share no prefix:\n%s", out)
	}
}

// One group is a wrapper around the whole table and buys nothing.
func TestPageDoesNotGroupWhenEveryLabelSharesOnePrefix(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a"},
		Rows: []map[string]string{
			{"fact": "alpha.one", "run-a": "1"},
			{"fact": "alpha.two", "run-a": "2"},
		},
	})

	if strings.Contains(out, `class="group"`) {
		t.Fatalf("page grouped a single prefix:\n%s", out)
	}
}

// Digits line up or they cannot be compared, which is the whole job of a row
// read across four runs. Alignment is typographic and says nothing about what a
// number means - a row is set right when every reading in it is a number.
func TestPageAlignsARowOfNumbers(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a", "run-b"},
		Rows: []map[string]string{
			{"fact": "alpha.count", "run-a": "29926637", "run-b": "29996738"},
			{"fact": "alpha.note", "run-a": "yes", "run-b": "no"},
		},
	})

	if !strings.Contains(out, `<td class="num">29926637</td>`) {
		t.Fatalf("page does not set the numeric row as numbers:\n%s", out)
	}
	if strings.Contains(out, `<td class="num">yes</td>`) {
		t.Fatalf("page set a word as a number:\n%s", out)
	}
}

// A column of numbers is the same case turned ninety degrees: the step views
// are read down a column, not across a row.
func TestPageAlignsAColumnOfNumbers(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "tsv",
		Columns: []string{"seq", "seconds", "claim"},
		Rows: []map[string]string{
			{"seq": "1", "seconds": "80", "claim": "done"},
			{"seq": "2", "seconds": "6", "claim": "also done"},
		},
	})

	if !strings.Contains(out, `<td class="num">80</td>`) {
		t.Fatalf("page does not set the numeric column as numbers:\n%s", out)
	}
	if strings.Contains(out, `<td class="num">done</td>`) {
		t.Fatalf("page set a claim as a number:\n%s", out)
	}
}

// An absent cell is not a number and must keep saying it is absent, whatever
// the rest of its row happens to hold.
func TestPageLeavesAnAbsentCellAloneInANumericRow(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a", "run-b"},
		Rows:    []map[string]string{{"fact": "alpha.count", "run-b": "7"}},
	})

	if !strings.Contains(out, `<td class="absent">-</td>`) {
		t.Fatalf("page lost the absence marker:\n%s", out)
	}
}

// THE FILTER IS THE READER'S, AND IT IS NOT ON. A page that muted rows by
// default would be applying a computation over values before anyone asked, and
// the reader could not tell what had been decided for them. The control says
// exactly what it compares, and the words `changed` and `unchanged` are the
// fingerprint layer's - a projection does not borrow an instrument's vocabulary.
func TestPageShowsEveryRowUntilAReaderAsksOtherwise(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a", "run-b"},
		Rows: []map[string]string{
			{"fact": "alpha.same", "run-a": "1", "run-b": "1"},
			{"fact": "alpha.differs", "run-a": "1", "run-b": "2"},
		},
	})

	// The controls themselves ARE hidden until script reveals them, because a
	// collapse button that cannot collapse is worse than none. What must not be
	// hidden is a row: no group is shut and no filter is applied.
	for _, applied := range []string{"shut", "filtered"} {
		if strings.Contains(out, `class="`+applied) {
			t.Fatalf("page applied %q before it was asked to:\n%s", applied, out)
		}
	}
	for _, label := range []string{"alpha.same", "alpha.differs"} {
		if !strings.Contains(out, label) {
			t.Fatalf("page dropped row %q:\n%s", label, out)
		}
	}
	for _, word := range []string{"unchanged", "changed"} {
		if strings.Contains(strings.ToLower(out), word) {
			t.Fatalf("page borrows the fingerprint layer's word %q:\n%s", word, out)
		}
	}
}

// The filter needs to know which rows it would keep, and that mark is on the
// row rather than computed in script, so what the page claims is in the markup.
func TestPageMarksWhichRowsHoldMoreThanOneValue(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a", "run-b"},
		Rows: []map[string]string{
			{"fact": "alpha.same", "run-a": "1", "run-b": "1"},
			{"fact": "alpha.differs", "run-a": "1", "run-b": "2"},
		},
	})

	if got := strings.Count(out, `data-varies="yes"`); got != 1 {
		t.Fatalf("page marks %d rows as holding more than one value, want 1:\n%s", got, out)
	}
}

// A run id is a citation. Nothing on the page may show a shortened one, because
// a reader copies what they see and a bare timestamp names no run.
func TestPageNeverShortensAColumnName(t *testing.T) {
	t.Parallel()

	id := "survey-20260101T000100Z"
	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", id},
		Rows:    []map[string]string{{"fact": "alpha.one", id: "1"}},
	})

	if !strings.Contains(out, ">"+id+"<") {
		t.Fatalf("page does not carry the column name whole:\n%s", out)
	}
	if strings.Contains(out, "\u2026") {
		t.Fatalf("page shortened something with an ellipsis:\n%s", out)
	}
}

func headOf(s string) string {
	if len(s) > 900 {
		return s[:900]
	}
	return s
}

// A row every column reads the same is that value said once. Nothing is hidden
// and nothing is muted - the text is there, and so is the fact that each column
// carries it. Four copies of one path across a table is the reader doing the
// comparing that the row already answers.
func TestPageSaysARowOnceWhenEveryColumnReadsTheSame(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a", "run-b", "run-c"},
		Rows:    []map[string]string{{"fact": "alpha.same", "run-a": "x", "run-b": "x", "run-c": "x"}},
	})

	if got := strings.Count(out, ">x<"); got != 1 {
		t.Fatalf("page repeats the value %d times, want once:\n%s", got, out)
	}
	if !strings.Contains(out, `colspan="3"`) {
		t.Fatalf("page does not span the shared value across its columns:\n%s", out)
	}
	if !strings.Contains(out, "all 3") {
		t.Fatalf("page does not say how many columns read the same:\n%s", out)
	}
}

// One column is not agreement across columns, so a single-record page never
// collapses anything.
func TestPageDoesNotCollapseASingleColumn(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "tsv",
		Columns: []string{"seq", "claim"},
		Rows:    []map[string]string{{"seq": "1", "claim": "done"}},
	})

	if strings.Contains(out, "colspan") {
		t.Fatalf("page collapsed a row that has one column:\n%s", out)
	}
}

// A heading over one row costs a row to save none. Prefixes with a single
// member are left ungrouped rather than given a header of their own.
func TestPageDoesNotHeadAGroupOfOne(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a"},
		Rows: []map[string]string{
			{"fact": "alpha.one", "run-a": "1"},
			{"fact": "alpha.two", "run-a": "2"},
			{"fact": "beta.only", "run-a": "3"},
			{"fact": "gamma.one", "run-a": "4"},
			{"fact": "gamma.two", "run-a": "5"},
		},
	})

	if strings.Contains(out, ">beta<") {
		t.Fatalf("page headed a group holding one row:\n%s", out)
	}
	for _, want := range []string{">alpha<", ">gamma<"} {
		if !strings.Contains(out, want) {
			t.Fatalf("page lost the %s group:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "beta.only") {
		t.Fatalf("page dropped the ungrouped row:\n%s", out)
	}
}
