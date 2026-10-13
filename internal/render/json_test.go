package render_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/render"
)

func renderJSON(data io.Writer, rec *record.Record, view string) error {
	doc, err := render.ProjectFiltered(rec, view, nil)
	if err != nil {
		return err
	}
	return render.DocumentJSON(data, doc)
}

func decoded(t *testing.T, body []byte) render.Document {
	t.Helper()

	var doc render.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not json: %v\n%s", err, body)
	}
	return doc
}

func TestJSONCarriesTheSameValuesAsTheColumns(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	rec := built(t, true, step{label: "install", rc: "0", seconds: "3", drift: "unchanged", claim: "Installed 1.0."})
	if err := renderJSON(&out, rec, "tsv"); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	doc := decoded(t, out.Bytes())
	if doc.View != "tsv" {
		t.Errorf("view = %q", doc.View)
	}
	if got := strings.Join(doc.Columns, ","); got != "seq,host,step,exit,seconds,drift,claim" {
		t.Errorf("columns = %q", got)
	}
	if len(doc.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(doc.Rows))
	}
	row := doc.Rows[0]
	for field, want := range map[string]string{
		"seq": "1", "host": "alpha", "step": "install",
		"exit": "0", "seconds": "3", "drift": "no", "claim": "Installed 1.0.",
	} {
		if row[field] != want {
			t.Errorf("%s = %q, want %q", field, row[field], want)
		}
	}
}

func TestJSONNeverInventsSuccessEither(t *testing.T) {
	t.Parallel()

	// The -1 rule is the record's, and a second encoding of the same view must
	// not soften it on the way out.
	var out bytes.Buffer
	rec := built(t, true, step{label: "install", rc: "abc", seconds: "later", drift: "maybe", claim: "fine"})
	if err := renderJSON(&out, rec, "tsv"); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	row := decoded(t, out.Bytes()).Rows[0]
	if row["exit"] != "-1" {
		t.Errorf("exit = %q, want -1", row["exit"])
	}
	if row["seconds"] != "-1" {
		t.Errorf("seconds = %q, want -1", row["seconds"])
	}
	if row["drift"] != "?" {
		t.Errorf("drift = %q, want ?", row["drift"])
	}
}

func TestNoticesTravelInTheDocumentNotBesideIt(t *testing.T) {
	t.Parallel()

	// A json consumer reads one stream. A warning on the other one is a warning
	// nobody reads.
	var out bytes.Buffer
	rec := built(t, false, step{label: "install", rc: "0", seconds: "1", drift: "unchanged", claim: "ok"})
	if err := renderJSON(&out, rec, "tsv"); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	doc := decoded(t, out.Bytes())
	if len(doc.Notices) == 0 {
		t.Fatal("an interrupted run rendered without saying so")
	}
	if !strings.Contains(strings.Join(doc.Notices, " "), "interrupted") {
		t.Errorf("notices = %v, want the interruption named", doc.Notices)
	}
}

func TestAViewWithNothingToSayStillCarriesItsShape(t *testing.T) {
	t.Parallel()

	// An empty rows array and an absent one are different answers, and a
	// consumer indexing into null is the second failure.
	var out bytes.Buffer
	rec := built(t, true)
	if err := renderJSON(&out, rec, "tsv"); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	if !bytes.Contains(out.Bytes(), []byte(`"rows": []`)) {
		t.Errorf("empty view rendered as %s", out.String())
	}
}

func TestAnUnknownViewIsRefusedRatherThanRenderedEmpty(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := renderJSON(&out, built(t, true), "phase-of-the-moon"); err == nil {
		t.Fatal("an unknown view rendered")
	}
}
