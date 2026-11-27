package main

import (
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/record"
)

// Fyne grows a window to fit whatever its content asks for. Nothing on a
// screen - a record's output, a long label from someone's scenario - may push
// the window past the screen; a screen that asks for more scrolls instead.
func TestAScreenThatAsksForMoreRoomScrollsRatherThanGrowingTheWindow(t *testing.T) {
	test.NewTempApp(t)

	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	w.SetContent(s.content)

	huge := canvas.NewRectangle(nil)
	huge.SetMinSize(fyne.NewSize(5000, 3000))
	s.show(nil, huge)

	if got := s.content.MinSize(); got.Width > openingSize.Width || got.Height > openingSize.Height {
		t.Errorf("the window is asked for %v, want no more than %v", got, openingSize)
	}
}

// The sections are Runs, Batches, Campaigns and Settings. A record opened from
// a batch goes back to that batch, not to the Runs listing.
func TestARecordOpenedFromABatchGoesBackToTheBatch(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	smokeSet(t, runs, true)
	installCheck(t, runs, "install-check-20261123T171533Z-install-old")

	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, runs, "--runs-dir", func(string) {})
	w.SetContent(s.content)

	s.showBatches()
	if s.section != sectionBatches {
		t.Fatalf("section = %v", s.section)
	}
	s.batches.onOpen(s.batches.all[0])
	b, ok := s.current.(*batchView)
	if !ok {
		t.Fatalf("showing %T, want the batch", s.current)
	}
	b.open(0)
	d, ok := s.current.(*detailView)
	if !ok {
		t.Fatalf("showing %T, want the record", s.current)
	}
	if d.backLabel != "Batch" {
		t.Errorf("back says %q", d.backLabel)
	}
	d.back()
	if _, ok := s.current.(*batchView); !ok {
		t.Errorf("back went to %T, want the batch", s.current)
	}
}

// A run started from a scenario shows live under Activity, and the window
// asks before quitting while one is going.
func TestARunStartedHereIsShownUnderActivity(t *testing.T) {
	test.NewTempApp(t)
	t.Setenv(fakeEnv, "finish")

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, t.TempDir(), "--runs-dir", func(string) {})
	s.useTools(tools{testease: self, testeaseFrom: "--testease"})
	s.uiCall = func(func()) {}
	w.SetContent(s.content)

	s.startJob("run hello", []string{"run", "hello"})
	jv, ok := s.current.(*jobView)
	if !ok || len(s.jobs) != 1 || s.section != sectionActivity {
		t.Fatalf("showing %T, jobs %d, section %v", s.current, len(s.jobs), s.section)
	}
	jv.j.wait()
	jv.refresh()
	if text, _ := jv.statusText(); text != "measured" {
		t.Errorf("status = %q", text)
	}
	if got := jv.cellText(1, jcolStatus); got != "measured" {
		t.Errorf("step 2 = %q", got)
	}

	if jv.stop.Text != "Stop" || !jv.stop.Disabled() {
		t.Errorf("a finished job's button reads %q, disabled %v", jv.stop.Text, jv.stop.Disabled())
	}

	s.showActivity()
	a := s.current.(*activityView)
	if got := a.cellText(0, acolCommand); got != "run hello" {
		t.Errorf("activity = %q", got)
	}
}

// The sidebar highlights the section the screen showing belongs to, which is
// the one its back button names. A record opened over Home from the command
// palette says "Runs" and is under Runs; a comparison started from the Runs
// screen is under Runs; and a screen gone back to highlights its own section,
// not the one last visited.
func TestTheSidebarHighlightsTheSectionTheScreenBelongsTo(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	a := installCheck(t, runs, "install-check-20261120T100000Z")
	b := installCheck(t, runs, "install-check-20261121T100000Z")
	matrix := docsCompat(t, runs)

	w := test.NewWindow(nil)
	defer w.Close()
	s := newShell(w, runs, "--runs-dir", func(string) {})
	w.SetContent(s.content)
	if s.section != sectionHome {
		t.Fatalf("opens on %v, want Home", s.section)
	}

	// Opened over Home, as the palette opens a run.
	s.openRecord(matrix, "Runs", s.showRuns)
	m := s.current.(*detailView)
	if s.section != sectionRuns {
		t.Errorf("a record whose back says Runs is under %v", sectionNames[s.section])
	}
	// A cell of it keeps the matrix run's section.
	s.openRecord(filepath.Join(matrix, record.CellsDir, "1.0.10+7.2.6"), "Matrix", func() { s.show(m, m.content) })
	if s.current == m {
		t.Fatal("the cell did not open")
	}
	if s.section != sectionRuns {
		t.Errorf("a matrix cell is under %v, want Runs", sectionNames[s.section])
	}

	s.showHome()
	s.compareRecords([]string{a, b}, "Runs", s.showRuns)
	if _, ok := s.current.(*compareView); !ok || s.section != sectionRuns {
		t.Errorf("showing %T under %v, want a comparison under Runs", s.current, sectionNames[s.section])
	}

	// A record from Home, then somewhere else, then back to the record.
	s.showHome()
	s.openRecord(a, "Home", s.showHome)
	d := s.current.(*detailView)
	s.showScenarios()
	s.show(d, d.content)
	if s.section != sectionHome {
		t.Errorf("back on a record opened from Home, the sidebar highlights %v", sectionNames[s.section])
	}
}
