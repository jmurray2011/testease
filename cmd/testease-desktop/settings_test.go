package main

import (
	"path/filepath"
	"testing"
)

// There is no config file. Where records are is flag, then TESTEASE_RUNS_DIR,
// then the folder this app was last pointed at - a path is safe to remember,
// because it changes where things are and never what is measured.

func TestTheRunsDirectoryComesFromFlagThenEnvironmentThenMemory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		flag, env, remembered string
		want, from            string
	}{
		{"/f", "/e", "/r", "/f", "--runs-dir"},
		{"", "/e", "/r", "/e", "TESTEASE_RUNS_DIR"},
		{"", "", "/r", "/r", "last folder chosen here"},
		{"", "", "", "", ""},
	}
	for _, c := range cases {
		got, from := resolveRunsDir(c.flag, c.env, c.remembered)
		if got != c.want || from != c.from {
			t.Errorf("resolveRunsDir(%q, %q, %q) = %q from %q, want %q from %q",
				c.flag, c.env, c.remembered, got, from, c.want, c.from)
		}
	}
}

// The sidebar is narrow. The runs directory is shown as its own folder name,
// whole, with where it lives on a second line, rather than wrapped mid-word or
// cut off.
func TestTheSidebarShowsTheRunsDirectorysNameWhole(t *testing.T) {
	t.Parallel()

	sep := string(filepath.Separator)
	long := filepath.Join(sep+"Users", "jmurr", "projects", "personal", "testease-scenarios", "scenario-runs")
	name, in := sidebarPath(long)
	if name != "scenario-runs" || in != "in …"+sep+"testease-scenarios" {
		t.Errorf("sidebarPath(%q) = %q, %q", long, name, in)
	}
	if name, in := sidebarPath("runs"); name != "runs" || in != "" {
		t.Errorf("sidebarPath(runs) = %q, %q", name, in)
	}
}
