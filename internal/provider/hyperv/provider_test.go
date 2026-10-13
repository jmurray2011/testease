package hyperv

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/target"
)

// A one-shot target has no life between steps, so what tells two live cells
// apart is the name of the container each is running in right now.

func TestEveryCellAndEveryStepGetsItsOwnContainer(t *testing.T) {
	t.Parallel()

	first := containerName("sweep-20260812T140322Z", "6.2.4", "box", 1)
	second := containerName("sweep-20260812T140322Z", "6.2.40", "box", 1)
	later := containerName("sweep-20260812T140322Z", "6.2.4", "box", 2)

	if first == second {
		t.Errorf("cells 6.2.4 and 6.2.40 share %q", first)
	}
	if first == later {
		t.Errorf("two steps of one cell share %q: the container IS the command", first)
	}
}

func TestALongRunIdDoesNotCostACellItsIdentity(t *testing.T) {
	t.Parallel()

	long := "a-scenario-with-a-very-long-name-indeed-20260812T140322Z"
	first := containerName(long, "6.2.4", "box", 1)
	second := containerName(long, "6.2.40", "box", 1)

	if first == second {
		t.Errorf("two cells of a long-named run share %q", first)
	}
	if len(first) > 60 {
		t.Errorf("name is %d characters: %q", len(first), first)
	}
}

func TestAContainerNameSurvivesBeingOne(t *testing.T) {
	t.Parallel()

	got := containerName("sweep/2026", "6.2.4+el8", "box one", 1)
	if strings.ContainsAny(got, "/+ ") {
		t.Errorf("name = %q, want nothing docker will not take", got)
	}
}

func TestACarryIsOwnedAndStagedBeforeAOneShotStarts(t *testing.T) {
	t.Parallel()

	p := New(Options{ScratchRoot: t.TempDir()})
	c := &cell{provider: p, spec: target.Spec{Media: []target.Media{{Source: "declared.ps1", Dest: "/declared.ps1"}}}}
	receipt, err := c.ReceiveCarry(context.Background(), bytes.NewBufferString("exact dump bytes"), "/fixture.dump")
	if err != nil {
		t.Fatalf("ReceiveCarry: %v", err)
	}
	if !receipt.SeededBeforeStart {
		t.Fatal("one-shot receiver did not promise pre-start staging")
	}

	staged := c.stagedMedia()
	if len(staged) != 2 || staged[1].Remote != "/fixture.dump" {
		t.Fatalf("staged media = %+v, want declared media followed by the carry", staged)
	}
	got, err := os.ReadFile(staged[1].Local)
	if err != nil {
		t.Fatalf("read owned carry: %v", err)
	}
	if string(got) != "exact dump bytes" {
		t.Errorf("owned carry = %q", got)
	}
	if info, statErr := os.Stat(staged[1].Local); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("owned carry mode = %v, %v; want runner scratch to remain 0600", info, statErr)
	}

	if err := p.Release(context.Background(), c, false); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(staged[1].Local); !os.IsNotExist(err) {
		t.Errorf("owned carry remains after release: %v", err)
	}
}
