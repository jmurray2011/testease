package docker

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A remote drive's scenario travels as an archive, and the suite inside runs
// its scripts. A script that could run where it was written must still run
// there, and a document must not become something to run.
func TestARemoteDriveArchiveKeepsExecutableIntent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "suite.sh"), []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive, err := directoryArchive(dir, "/scenario", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]int64{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[header.Name] = header.Mode
	}
	if mode, ok := modes["scenario/suite.sh"]; !ok || mode&0o111 == 0 {
		t.Errorf("suite.sh mode = %o (present %v), want it executable", mode, ok)
	}
	if mode, ok := modes["scenario/notes.txt"]; !ok || mode&0o111 != 0 {
		t.Errorf("notes.txt mode = %o (present %v), want it not executable", mode, ok)
	}
}

// The destination is a path in a Linux container, whatever the runner is. A
// backslash there is part of a name, not a Windows separator to convert.
func TestARemoteDriveArchiveKeepsTheContainerPathAsWritten(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "suite.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := directoryArchive(dir, `/srv/suite\one`, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	var names []string
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	if len(names) == 0 || names[0] != `srv/suite\one` {
		t.Errorf("archive names = %q, want them under srv/suite\\one", names)
	}
}
