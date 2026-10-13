package main

import (
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/record"
)

func TestARecordedViewSelectsTheSameProjectionAcrossRuns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	recs := []*record.Record{
		viewRecord(t, root, "first-20260902T120000Z", record.ViewRef{Kind: "facts", Facts: []string{"evidence.sha256", "target.version"}}),
		viewRecord(t, root, "second-20260902T120001Z", record.ViewRef{Kind: "facts", Facts: []string{"evidence.sha256", "target.version"}}),
	}
	kind, facts, err := recordedView(recs, "team")
	if err != nil {
		t.Fatalf("recordedView: %v", err)
	}
	if kind != "facts" || len(facts) != 2 || facts[0] != "evidence.sha256" {
		t.Errorf("kind=%q facts=%v", kind, facts)
	}
}

func TestAViewThatDriftedBetweenRecordsIsRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	recs := []*record.Record{
		viewRecord(t, root, "first-20260902T120000Z", record.ViewRef{Kind: "facts", Facts: []string{"evidence.sha256"}}),
		viewRecord(t, root, "second-20260902T120001Z", record.ViewRef{Kind: "facts", Facts: []string{"another.fact"}}),
	}
	if _, _, err := recordedView(recs, "team"); err == nil {
		t.Fatal("records with different definitions of one named view were accepted")
	}
}

func viewRecord(t *testing.T, root, id string, view record.ViewRef) *record.Record {
	t.Helper()
	dir := filepath.Join(root, id)
	w, err := record.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Emit(record.KindRunStarted, 0, record.RunStarted{
		RunID: id, Views: map[string]record.ViewRef{"team": view},
	}); err != nil {
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
