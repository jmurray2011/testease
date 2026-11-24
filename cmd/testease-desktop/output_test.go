package main

import (
	"testing"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// Output can be searched, and the first line that looks like trouble can be
// found without reading from the top.

func TestMatchesAreFoundOnEveryLineWhateverTheCase(t *testing.T) {
	text := "Starting\nconnection refused\nretry: Connection REFUSED again\n"
	got := findMatches(text, "refused")
	want := []match{{Row: 1, Col: 11, Len: 7}, {Row: 2, Col: 18, Len: 7}}
	if len(got) != len(want) {
		t.Fatalf("matches %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(findMatches(text, "")) != 0 {
		t.Error("an empty search matches")
	}
}

// Columns count characters, not bytes, as the grid lays them out.
func TestAMatchAfterWideCharactersIsWhereTheGridDrawsIt(t *testing.T) {
	got := findMatches("✓ ok — done", "done")
	if len(got) != 1 || got[0].Col != 7 {
		t.Errorf("matches %+v", got)
	}
}

func TestTheFirstTroubleLineIsFound(t *testing.T) {
	text := "pulling image\nerrors: 0\nstarting\nERROR: unit failed to start\npanic: x\n"
	if got := firstTrouble(text); got != 3 {
		t.Errorf("first trouble on line %d, want 3", got)
	}
	if got := firstTrouble("all good\n"); got != -1 {
		t.Errorf("clean output has trouble on line %d", got)
	}
}

func TestSearchingARecordsOutputCountsAndSteps(t *testing.T) {
	test.NewTempApp(t)
	runs := t.TempDir()
	dir := installCheck(t, runs, "install-check-20261123T171533Z")
	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatal(err)
	}
	v.search.debounce = func(f func()) { f() }
	v.steps.Select(0)
	v.tabs.SelectIndex(0)
	v.search.entry.SetText("app")
	if v.search.found.Text != "1 of 1" {
		t.Errorf("found %q", v.search.found.Text)
	}
	v.search.entry.SetText("nothing like this")
	if v.search.found.Text != "no match" {
		t.Errorf("found %q", v.search.found.Text)
	}
}

// Typing does not search on every key: the search waits for a pause, so a
// large output does not lag behind the keyboard.
func TestSearchWaitsForAPauseInTyping(t *testing.T) {
	test.NewTempApp(t)
	var ran int
	s := newOutputSearch(func() (*widget.TextGrid, *container.Scroll) { ran++; return nil, nil })
	s.debounce = func(f func()) { s.pending = f }
	s.entry.SetText("d")
	s.entry.SetText("do")
	s.entry.SetText("dog")
	if ran != 0 {
		t.Errorf("searched %d times while typing", ran)
	}
	s.pending()
	if ran != 1 {
		t.Errorf("searched %d times after the pause", ran)
	}
}
