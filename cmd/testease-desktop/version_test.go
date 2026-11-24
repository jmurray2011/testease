package main

import (
	"context"
	"errors"
	"runtime/debug"
	"testing"

	"fyne.io/fyne/v2/test"
)

// The app says which build it is and which build of testease it starts runs
// with, so a person can tell the two apart before a measurement depends on it.

func TestAStampedBuildIsNamedByItsStamp(t *testing.T) {
	if got := appVersion("v0.1.0", "0123456789abcdef", nil); got != "v0.1.0" {
		t.Errorf("version = %q", got)
	}
}

func TestAnUnstampedBuildIsNamedByItsCommit(t *testing.T) {
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
		{Key: "vcs.modified", Value: "true"},
	}}
	if got := appVersion("dev", "unknown", info); got != "dev (0123456789ab, modified)" {
		t.Errorf("version = %q", got)
	}
	if got := appVersion("dev", "unknown", nil); got != "dev" {
		t.Errorf("without build info, version = %q", got)
	}
}

func TestTheBinaryVersionIsReadFromItsAnswer(t *testing.T) {
	if got := binaryVersion([]byte("testease version v0.1.0\n")); got != "v0.1.0" {
		t.Errorf("version = %q", got)
	}
	if got := binaryVersion([]byte(" something else \n")); got != "something else" {
		t.Errorf("an unknown answer = %q", got)
	}
}

func settingsWithVersions(t *testing.T, app string, call cliCall) *settingsView {
	t.Helper()
	test.NewTempApp(t)
	v := newSettingsView("/srv/runs", "--runs-dir", func(string) string { return "" })
	v.async = func(f func()) { f() }
	v.onUI = func(f func()) { f() }
	v.addTools(tools{testease: "/opt/testease", testeaseFrom: "PATH"})
	v.showVersions(app, call)
	return v
}

func answers(out string) cliCall {
	return func(_ context.Context, args []string) ([]byte, error) {
		if len(args) != 1 || args[0] != "--version" {
			return nil, errors.New("asked " + args[0])
		}
		return []byte(out), nil
	}
}

func TestSettingsNamesBothVersions(t *testing.T) {
	v := settingsWithVersions(t, "v0.1.0", answers("testease version v0.1.0\n"))
	if v.appVersion.Text != "v0.1.0" || v.binaryVersion.Text != "v0.1.0" {
		t.Errorf("app %q, binary %q", v.appVersion.Text, v.binaryVersion.Text)
	}
	if v.mismatch.Visible() {
		t.Error("builds that match are called different")
	}
}

func TestSettingsSaysWhenTheBuildsDiffer(t *testing.T) {
	v := settingsWithVersions(t, "v0.1.0", answers("testease version v0.0.9\n"))
	if !v.mismatch.Visible() {
		t.Error("different builds are not called out")
	}
}

// A build from a working copy is not called different: it has no release to
// match.
func TestADevelopmentBuildIsNotCalledDifferent(t *testing.T) {
	v := settingsWithVersions(t, "dev (0123456789ab)", answers("testease version v0.0.9\n"))
	if v.mismatch.Visible() {
		t.Error("a development build is called different")
	}
}

func TestSettingsSaysWhenTheBinaryCannotBeAsked(t *testing.T) {
	v := settingsWithVersions(t, "v0.1.0", func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("no testease binary found")
	})
	if v.binaryVersion.Text != "could not ask: no testease binary found" {
		t.Errorf("binary = %q", v.binaryVersion.Text)
	}
	if v.mismatch.Visible() {
		t.Error("an unknown version is called different")
	}
}
