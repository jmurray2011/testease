package main

import (
	"runtime/debug"
	"testing"
)

func TestToolRefUsesEmbeddedBuildInfoWhenLinkerStampIsAbsent(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.1-0.20261013235735-da4c2ed3abe0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "da4c2ed3abe00fceb988d1129a13872eedb45c67"},
			{Key: "vcs.modified", Value: "false"},
		},
	}

	got := toolRefFromBuild("dev", "unknown", info)
	if got.Version != info.Main.Version || got.Commit != "da4c2ed3abe00fceb988d1129a13872eedb45c67" {
		t.Fatalf("tool ref = %+v, want embedded module version and VCS revision", got)
	}
}

func TestToolRefKeepsExplicitStampAndMarksModifiedFallback(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "da4c2ed3abe00fceb988d1129a13872eedb45c67"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	got := toolRefFromBuild("dev", "unknown", info)
	if got.Version != "dev-dirty" || got.Commit != "da4c2ed3abe00fceb988d1129a13872eedb45c67" {
		t.Fatalf("tool ref = %+v, want dirty VCS fallback", got)
	}

	got = toolRefFromBuild("v1.2.3", "stamped-commit", info)
	if got.Version != "v1.2.3" || got.Commit != "stamped-commit" {
		t.Fatalf("tool ref = %+v, want linker stamp to take precedence", got)
	}
}

func TestToolRefDoesNotDuplicateEmbeddedDirtyMarker(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.1-0.20261021001703-9027d2b1c816+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "9027d2b1c81660e1f8b74a90746eee9b242c9824"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	got := toolRefFromBuild("dev", "unknown", info)
	if got.Version != info.Main.Version {
		t.Fatalf("tool version = %q, want the existing dirty marker unchanged", got.Version)
	}
}
