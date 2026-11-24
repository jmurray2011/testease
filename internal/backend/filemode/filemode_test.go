package filemode

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func write(t *testing.T, body string, mode os.FileMode) (string, os.FileInfo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info
}

func TestAnExecutableScriptIsExecutable(t *testing.T) {
	t.Parallel()

	path, info := write(t, "#!/bin/sh\ntrue\n", 0o755)
	if !Executable(path, info) {
		t.Error("an executable script with a shebang was not executable")
	}
}

func TestAnExecutableLinuxBinaryIsExecutable(t *testing.T) {
	t.Parallel()

	path, info := write(t, "\x7fELF\x02\x01\x01", 0o755)
	if !Executable(path, info) {
		t.Error("an executable ELF binary was not executable")
	}
}

func TestPlainBytesAreNotExecutable(t *testing.T) {
	t.Parallel()

	path, info := write(t, "a document", 0o644)
	if Executable(path, info) {
		t.Error("plain bytes without an executable bit were executable")
	}
}

// Windows has no executable bit, so a file declares it can be run by what it
// starts with - the only intent a Linux target can act on. On unix the bit
// says so, and a shebang alone does not.
func TestAShebangIsTheIntentWhereThereIsNoBit(t *testing.T) {
	t.Parallel()

	path, info := write(t, "#!/bin/sh\ntrue\n", 0o644)
	want := runtime.GOOS == "windows"
	if got := Executable(path, info); got != want {
		t.Errorf("Executable = %v for a shebang without the bit, want %v on %s", got, want, runtime.GOOS)
	}
}

func TestADirectoryIsNotAnExecutableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if Executable(dir, info) {
		t.Error("a directory was an executable file")
	}
}
