package private

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestMkdirMakesADirectoryOnlyItsOwnerCanUse(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "secrets")
	if err := Mkdir(dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := Verify(dir, 0o700); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestMkdirRefusesADirectoryThatIsAlreadyThere(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := Mkdir(dir); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Mkdir over an existing directory = %v, want fs.ErrExist", err)
	}
}

func TestMkdirAllMakesItsParentsAndAPrivateLeaf(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "runtime", "run", "scratch")
	if err := MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := Verify(dir, 0o700); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if err := MkdirAll(dir); err != nil {
		t.Errorf("MkdirAll over an existing directory: %v", err)
	}
}

func TestMkdirTempMakesAFreshPrivateDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first, err := MkdirTemp(root, "carry-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	second, err := MkdirTemp(root, "carry-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if first == second {
		t.Fatalf("MkdirTemp returned %s twice", first)
	}
	if filepath.Dir(first) != root || filepath.Base(first)[:len("carry-")] != "carry-" {
		t.Errorf("MkdirTemp = %s, want carry-* in %s", first, root)
	}
	if err := Verify(first, 0o700); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// A file written into a private directory is private: on unix by its own
// mode, on Windows because it takes the directory's owner-only access.
func TestAFileInAPrivateDirectoryIsPrivate(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "secrets")
	if err := Mkdir(dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "owner.json")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(file, 0o600); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyRefusesAnOrdinaryFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "public")
	if err := os.WriteFile(file, []byte("anyone"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(file, 0o600); err == nil {
		t.Error("Verify accepted a file others can read")
	}
}
