package runner

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/scenario"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// A READING THE READER DROPPED IS NOT A READING NOBODY TOOK. The suite writes
// its facts to a file and the scanner that reads it back is bounded at 4MB. A
// line past that bound stopped the scan, kept the facts read so far, and said
// so only in a log line - so the record could not tell a fact that was
// measured and dropped from one that was never measured at all.
func TestASuiteLinePastTheScanBoundIsReportedIntoTheRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, factsFile)
	huge := strings.Repeat("x", 5*1024*1024)
	body := "fact\tapp.version\t7.2.3\n" + "fact\thuge\t" + huge + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write facts: %v", err)
	}

	// readFacts already returns the error; the defect is one layer up, where
	// it became a log line and nothing in the record.
	facts, err := readFacts(path)
	if err == nil {
		t.Fatal("a line past the scan bound was tolerated silently")
	}
	if len(facts) != 1 || facts[0].Key != "app.version" {
		t.Errorf("facts = %+v, want the reading taken before the bound was hit", facts)
	}

	r := &run{workspace: dir, opt: Options{Log: discardLog()}}
	got, warnings := r.collectDriven(1, scenario.Step{Label: "drive it"}, "", "")
	if len(got) != 1 {
		t.Errorf("facts = %+v, want the one taken before the bound", got)
	}
	if len(warnings) == 0 {
		t.Fatal("the record was told nothing about a fact set the reader truncated")
	}
	if !strings.Contains(strings.ToLower(warnings[0].Message), "scan") &&
		!strings.Contains(strings.ToLower(warnings[0].Message), "too long") {
		t.Errorf("warning = %+v, want it to say the scan stopped", warnings[0])
	}
}

// A stat that fails for any reason other than absence is not an answer. Read
// as "taken", every candidate name failed the same way and freshPath searched
// for a free one forever.
func TestAPathThatCannotBeStattedDoesNotSearchForever(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root stats through a mode-000 directory, so there is no unreadable path to offer")
	}

	dir := t.TempDir()
	closed := filepath.Join(dir, "closed")
	if err := os.Mkdir(closed, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })

	r := &run{dir: dir, opt: Options{Log: discardLog()}}
	done := make(chan string, 1)
	go func() { done <- r.freshPath("closed/steps/1-one.stdout") }()

	select {
	case got := <-done:
		if got == "" {
			t.Error("freshPath returned no name")
		}
	case <-timeAfter():
		t.Fatal("freshPath never returned: a stat it could not answer reads as a name already taken")
	}
}

// timeAfter is the bound this test is willing to wait. Two seconds is
// generous: the defect is an unbounded loop, not a slow one.
func timeAfter() <-chan time.Time { return time.After(2 * time.Second) }
