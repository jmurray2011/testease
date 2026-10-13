package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/store"
)

// Discovery is host-dependent, and that is exactly why what it found gets
// written into the record. Two people with different stores get differently
// scoped sweeps whose records read identically unless the set is written down.

func stocked(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("media"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestAGlobFindsWhatIsThere(t *testing.T) {
	t.Parallel()

	dir := stocked(t, "app-1.0.pkg", "app-2.0.pkg", "app-10.0.pkg", "notes.txt")

	found, err := store.Discover(dir, "@store:app-*.pkg")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if strings.Join(found, ",") != "app-1.0.pkg,app-2.0.pkg,app-10.0.pkg" {
		t.Errorf("found = %v, want the three packages in version order", found)
	}
}

func TestVersionsSortTheWayAPersonReadsThem(t *testing.T) {
	t.Parallel()

	// A sweep enumerated as 1.0, 10.0, 2.0 reads as though somebody chose that
	// order. Nobody did; it is what sorting version-shaped strings as text
	// does, and the census that comes out is unreadable.
	dir := stocked(t, "app-1.9.pkg", "app-1.10.pkg", "app-1.2.pkg")

	found, err := store.Discover(dir, "@store:app-*.pkg")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if strings.Join(found, ",") != "app-1.2.pkg,app-1.9.pkg,app-1.10.pkg" {
		t.Errorf("found = %v", found)
	}
}

func TestFindingNothingIsSaidOutLoud(t *testing.T) {
	t.Parallel()

	// A fixed ladder silently skips a rung whose media is missing and reports
	// a complete sweep over an incomplete set. Discovering nothing at all is
	// the same failure, larger, so it refuses rather than sweeping zero cells.
	_, err := store.Discover(stocked(t, "something-else"), "@store:app-*.pkg")
	if err == nil {
		t.Fatal("a sweep over nothing was allowed to start")
	}
	if !strings.Contains(err.Error(), "app-*.pkg") {
		t.Errorf("error %q does not say what it looked for", err)
	}
}

func TestAStoreThatIsNotThereIsNotAnEmptyStore(t *testing.T) {
	t.Parallel()

	_, err := store.Discover(filepath.Join(t.TempDir(), "no-such-store"), "@store:*.pkg")
	if err == nil {
		t.Fatal("a missing store read as one holding nothing")
	}
}

func TestSomethingThatIsNotADiscoveryIsLeftAlone(t *testing.T) {
	t.Parallel()

	if store.IsDiscovery("1.0,2.0,3.0") {
		t.Error("a plain list was taken for a discovery")
	}
	if !store.IsDiscovery("@store:app-*.pkg") {
		t.Error("a discovery was not recognised")
	}
}

func TestAPatternCannotClimbOutOfTheStore(t *testing.T) {
	t.Parallel()

	if _, err := store.Discover(stocked(t, "app-1.0.pkg"), "@store:../*"); err == nil {
		t.Fatal("a pattern reached outside the store")
	}
}
