package main

import (
	"context"
	"runtime/debug"
	"strings"
)

// version and commit are stamped at build time, as the CLI's are: make
// desktop and the release workflow set them from git.
var (
	version = "dev"
	commit  = "unknown"
)

// appVersion names this build: its stamp, or for a build from a working copy
// the commit Go recorded, so two development builds can still be told apart.
func appVersion(stampedVersion, stampedCommit string, info *debug.BuildInfo) string {
	if stampedVersion != "dev" {
		return stampedVersion
	}
	rev, modified := stampedCommit, false
	if info != nil {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if rev == "unknown" {
					rev = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if rev == "" || rev == "unknown" {
		return stampedVersion
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified {
		rev += ", modified"
	}
	return stampedVersion + " (" + rev + ")"
}

// thisVersion is appVersion for the running binary.
func thisVersion() string {
	info, _ := debug.ReadBuildInfo()
	return appVersion(version, commit, info)
}

// binaryVersion reads the version out of testease --version.
func binaryVersion(out []byte) string {
	text := strings.TrimSpace(string(out))
	return strings.TrimSpace(strings.TrimPrefix(text, "testease version"))
}

// released says whether a version names a tagged build, the only kind a
// different one is worth pointing out for.
func released(v string) bool { return strings.HasPrefix(v, "v") }

// showVersions names this build and asks the testease binary for its own.
// The asking is off the app's goroutine: the binary is another program.
func (v *settingsView) showVersions(app string, call cliCall) {
	v.appVersion.SetText(app)
	v.binaryVersion.SetText("asking...")
	v.async(func() {
		out, err := call(context.Background(), []string{"--version"})
		v.onUI(func() {
			if err != nil {
				v.binaryVersion.SetText("could not ask: " + err.Error())
				return
			}
			got := binaryVersion(out)
			v.binaryVersion.SetText(got)
			if released(app) && released(got) && got != app {
				v.mismatch.Show()
			}
		})
	})
}
