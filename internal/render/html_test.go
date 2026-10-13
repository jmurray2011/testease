package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/render"
)

// rendered page, as a string. HTML has no second channel: a page is one stream,
// so what the text views send to stderr has to be in here and unmistakable.
func page(t *testing.T, doc render.Document) string {
	t.Helper()

	var b bytes.Buffer
	if err := render.HTML(&b, doc, "a title", "a footer"); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	return b.String()
}

// A value is whatever a probe wrote on fd 3, which is to say arbitrary bytes
// from a machine under test. The gate's other cases feed hostile input to the
// tsv view for the same reason.
func TestHTMLEscapesWhatAProbeWrote(t *testing.T) {
	t.Parallel()

	hostile := `<script>alert(1)</script>`
	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a"},
		Rows:    []map[string]string{{"fact": "alpha.note", "run-a": hostile}},
	})

	if strings.Contains(out, hostile) {
		t.Fatalf("page carries an unescaped script tag:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("page does not carry the escaped value:\n%s", out)
	}
}

// A key rendered as an empty cell reads as an empty reading, and the dash is
// real text so the table can be copied out of.
func TestHTMLRendersAnAbsentColumnAsADash(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a", "run-b"},
		Rows:    []map[string]string{{"fact": "alpha.count", "run-b": "7"}},
	})

	if !strings.Contains(out, `<td class="absent">-</td>`) {
		t.Fatalf("page does not mark the absent column:\n%s", out)
	}
}

// Nothing is styled by what a value says. A cell is marked only where the
// document has no entry for it, which is structure rather than text - a reading
// whose measured value happens to be `null` is a reading, not an absence.
func TestHTMLMarksOnlyAbsenceAndNotAValue(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "facts",
		Columns: []string{"fact", "run-a"},
		Rows:    []map[string]string{{"fact": "alpha.reading", "run-a": "null"}},
	})

	if strings.Contains(out, `<td class="absent">null</td>`) {
		t.Fatalf("page styled a recorded value as an absence:\n%s", out)
	}
	if !strings.Contains(out, "<td>null</td>") {
		t.Fatalf("page does not carry the recorded value plainly:\n%s", out)
	}
}

// The warning channel has to stay out of the data. In a page that means outside
// the table element, not a row inside it.
func TestHTMLKeepsNoticesOutOfTheTable(t *testing.T) {
	t.Parallel()

	const notice = "this run was interrupted"
	out := page(t, render.Document{
		View:    "tsv",
		Columns: []string{"seq", "claim"},
		Rows:    []map[string]string{{"seq": "1", "claim": "done"}},
		Notices: []string{notice},
	})

	at := strings.Index(out, notice)
	table := strings.Index(out, "<table")
	if at < 0 {
		t.Fatalf("page drops the notice entirely:\n%s", out)
	}
	if table < 0 || at > table {
		t.Fatalf("notice at %d is not before the table at %d:\n%s", at, table, out)
	}
}

// A view with rows and no columns cannot be laid out, and guessing one would
// invent a heading nothing measured.
func TestHTMLRefusesADocumentWithNoColumns(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	err := render.HTML(&b, render.Document{View: "facts"}, "t", "f")
	if err == nil {
		t.Fatalf("HTML accepted a document with no columns; wrote %q", b.String())
	}
	if !strings.Contains(err.Error(), "facts") {
		t.Fatalf("error = %v, want the view named", err)
	}
}

// An empty view says so rather than rendering a table with nothing in it.
func TestHTMLSaysWhenAViewHasNoRows(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{View: "rows", Columns: []string{"cell", "status"}})

	if strings.Contains(out, "<table") {
		t.Fatalf("page rendered an empty table:\n%s", out)
	}
	if !strings.Contains(out, "no rows") {
		t.Fatalf("page does not say the view is empty:\n%s", out)
	}
}

// The first column is the row's name in every view this tool has, and it is the
// one that stays put while the rest scroll.
func TestHTMLMakesTheFirstColumnTheRowLabel(t *testing.T) {
	t.Parallel()

	out := page(t, render.Document{
		View:    "rows",
		Columns: []string{"cell", "status"},
		Rows:    []map[string]string{{"cell": "alpha", "status": "measured"}},
	})

	if !strings.Contains(out, `<th class="label" scope="row">alpha</th>`) {
		t.Fatalf("page does not head the row with its first column:\n%s", out)
	}
}
