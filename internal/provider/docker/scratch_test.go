package docker_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	provider "github.com/jmurray2011/testease/internal/provider/docker"
	"github.com/jmurray2011/testease/internal/target"
)

// ONE CELL RELEASING MUST NOT DESTROY ANOTHER'S BEFORE-SNAPSHOT.
//
// A fingerprint reading is taken on the runner's disk, and cells of a census
// run in parallel. When that scratch was shared per PROVIDER, the first cell
// to finish removed the directory and every cell still between its before and
// its after lost the reading it was going to compare against. The drift those
// cells then reported was not a fact about any machine.
//
// The scratch is per container now, and this holds it there. Live: it needs a
// daemon, because the thing under test is what Release does to real files
// while another target is mid-measurement.
func TestReleasingOneCellLeavesAnothersReadingWhereItWas(t *testing.T) {
	t.Parallel()

	client := requireDocker(t)
	instrument := fakeInstrument(t)
	id := runID(t)

	p := provider.New(provider.Options{
		Client: client, RunID: id, Runner: "testease-test",
		Instrument: &instrument, Notices: io.Discard,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	t.Cleanup(func() { removeNetworks(t, client, id) })

	// Two cells of one census, from one provider, as a sweep runs them.
	first, err := p.Resolve(ctx, target.Spec{Host: "box", Cell: "one", Image: image()})
	if err != nil {
		t.Fatalf("resolve the first cell: %v", err)
	}
	second, err := p.Resolve(ctx, target.Spec{Host: "box", Cell: "two", Image: image()})
	if err != nil {
		_ = p.Release(context.Background(), first, false)
		t.Fatalf("resolve the second cell: %v", err)
	}
	t.Cleanup(func() { _ = p.Release(context.Background(), second, false) })

	// The second cell takes its BEFORE and is now mid-measurement.
	printer, ok := second.(target.Fingerprinter)
	if !ok {
		t.Fatal("a configured instrument did not make the target a Fingerprinter")
	}
	before, err := printer.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := os.Stat(before.Ref); err != nil {
		t.Fatalf("the reading was not written: %v", err)
	}

	// The first cell finishes and is released.
	if err := p.Release(ctx, first, false); err != nil {
		t.Fatalf("Release the first cell: %v", err)
	}

	// The second cell's before-snapshot is still there to compare against.
	if _, err := os.Stat(before.Ref); err != nil {
		t.Fatalf("releasing one cell destroyed another's before-snapshot: %v", err)
	}
	change, err := printer.Diff(ctx, before)
	if err != nil {
		t.Fatalf("Diff after a neighbour was released: %v", err)
	}
	if change.Result != target.Unchanged {
		t.Errorf("result = %q, want %q: nothing happened to this box", change.Result, target.Unchanged)
	}
}

// And its own readings do go when it is released: they are working material,
// not evidence, and leaving them would put unlabelled files somewhere reap
// cannot reason about.
func TestACellsOwnReadingsGoWhenItIsReleased(t *testing.T) {
	t.Parallel()

	client := requireDocker(t)
	instrument := fakeInstrument(t)
	id := runID(t)

	p := provider.New(provider.Options{
		Client: client, RunID: id, Runner: "testease-test",
		Instrument: &instrument, Notices: io.Discard,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	t.Cleanup(func() { removeNetworks(t, client, id) })

	tgt, err := p.Resolve(ctx, target.Spec{Host: "box", Image: image()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	before, err := tgt.(target.Fingerprinter).Snapshot(ctx)
	if err != nil {
		_ = p.Release(context.Background(), tgt, false)
		t.Fatalf("Snapshot: %v", err)
	}
	if err := p.Release(ctx, tgt, false); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(before.Ref); !os.IsNotExist(err) {
		t.Errorf("the released cell's own readings are still on disk (%v)", err)
	}
}
