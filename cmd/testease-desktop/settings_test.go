package main

import "testing"

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
