package docker

import "testing"

func TestEveryCellGetsItsOwnContainerName(t *testing.T) {
	t.Parallel()

	// A census runs many cells of one run at once. Two cells sharing a name
	// would collide, or worse, one would measure the other's machine.
	runID := "sweep-20260812T140322Z"
	first := containerName(runID, "6.2.4", "box")
	second := containerName(runID, "6.2.40", "box")

	if first == second {
		t.Fatalf("cells 6.2.4 and 6.2.40 share the container name %q", first)
	}
	if containerName(runID, "", "box") == first {
		t.Error("a single-target run and a cell share a name")
	}
}

func TestALongRunIdDoesNotCostTheCellItsIdentity(t *testing.T) {
	t.Parallel()

	long := "a-very-long-scenario-name-that-goes-on-and-on-20260812T140322Z"
	first := containerName(long, "6.2.4", "box")
	second := containerName(long, "6.2.40", "box")

	if first == second {
		t.Fatalf("two cells of a long-named run share %q", first)
	}
	if len(first) > 60 {
		t.Errorf("container name is %d characters: %q", len(first), first)
	}
}

func TestEveryCellGetsItsOwnNetwork(t *testing.T) {
	t.Parallel()

	// Hosts are reachable by the name the scenario gave them, which means two
	// cells of one census both alias "box". On a shared network docker accepts
	// both and answers either, so a probe would reach the other cell's machine
	// and nothing would report an error.
	runID := "sweep-20260812T140322Z"
	first := networkName(runID, "6.2.4")
	second := networkName(runID, "6.2.40")

	if first == second {
		t.Fatalf("cells 6.2.4 and 6.2.40 share the network %q", first)
	}
	if networkName(runID, "") == first {
		t.Error("a single-target run and a cell share a network")
	}
	if len(first) > 60 {
		t.Errorf("network name is %d characters: %q", len(first), first)
	}
}

func TestALongRunIdDoesNotCostTheCellItsNetwork(t *testing.T) {
	t.Parallel()

	long := "a-very-long-scenario-name-that-goes-on-and-on-20260812T140322Z"
	first := networkName(long, "6.2.4")
	second := networkName(long, "6.2.40")

	if first == second {
		t.Fatalf("two cells of a long-named run share the network %q", first)
	}
	if len(first) > 60 {
		t.Errorf("network name is %d characters: %q", len(first), first)
	}
}
