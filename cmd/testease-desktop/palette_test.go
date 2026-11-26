package main

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// Ctrl+K finds any screen, scenario or run by typing part of its name.

func TestThePaletteMatchesEveryWordTyped(t *testing.T) {
	items := []paletteItem{
		{Title: "Runs", Kind: "Go to"},
		{Title: "vt7-operations-20261123T133133Z", Kind: "Open run"},
		{Title: "vt7-operations", Kind: "Run scenario"},
		{Title: "docs-compat-20261106T001452Z", Kind: "Open run"},
	}
	got := matchItems(items, "vt7 ops")
	if len(got) != 0 {
		t.Errorf("a word that is not there matched: %+v", got)
	}
	got = matchItems(items, "vt7 operations")
	if len(got) != 2 || got[0].Title != "vt7-operations" {
		t.Errorf("matches %+v", got)
	}
	if got := matchItems(items, "run"); got[0].Title != "Runs" {
		t.Errorf("a title that starts with the words is not first: %+v", got)
	}
	if got := matchItems(items, ""); len(got) != len(items) {
		t.Errorf("nothing typed lists %d", len(got))
	}
}

func TestThePaletteOffersScreensScenariosAndRuns(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	installCheck(t, runs, "install-check-20261121T080000Z")
	s := shellWithFake(t, runs)
	s.tools.scenarioRoot = exampleRoot(t)
	kinds := map[string]bool{}
	var open func()
	for _, it := range s.paletteItems() {
		kinds[it.Kind] = true
		if it.Title == "install-check-20261121T080000Z" {
			open = it.Do
		}
	}
	for _, k := range []string{"Go to", "Run scenario", "Open run"} {
		if !kinds[k] {
			t.Errorf("no %q items", k)
		}
	}
	open()
	if _, ok := s.current.(*detailView); !ok {
		t.Errorf("opened %T", s.current)
	}
}
