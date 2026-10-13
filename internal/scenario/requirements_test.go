package scenario_test

import (
	"slices"
	"testing"

	"github.com/jmurray2011/testease/internal/scenario"
)

func TestHostRequirementsAreOrderedByHost(t *testing.T) {
	t.Parallel()

	plan := &scenario.Plan{
		Hosts: map[string]scenario.Host{
			"hotel": {}, "alpha": {}, "golf": {}, "bravo": {},
			"foxtrot": {}, "charlie": {}, "echo": {}, "delta": {},
		},
		Requires: []scenario.Capability{scenario.NeedsSession},
	}

	for range 20 {
		requirements := plan.HostRequirements()
		hosts := make([]string, 0, len(requirements))
		for _, requirement := range requirements {
			hosts = append(hosts, requirement.Host)
		}
		if !slices.IsSorted(hosts) {
			t.Fatalf("hosts = %v, want stable lexical order", hosts)
		}
	}
}
