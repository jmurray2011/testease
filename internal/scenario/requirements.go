package scenario

import (
	"fmt"
	"maps"
	"slices"
)

// HostRequirement is one execution contract implied by the resolved plan.
// AnyOf contains alternatives because an ordinary probe can use a session or
// a one-shot surface, and a carry destination can accept bytes directly or
// through a session.
type HostRequirement struct {
	Host  string
	Step  string
	AnyOf []Capability
}

// HostRequirements combines explicit scenario-wide requirements with the
// narrower requirements implied by each operation. It is shared by doctor and
// the runner so static advice and live refusal cannot drift apart.
func (p *Plan) HostRequirements() []HostRequirement {
	var out []HostRequirement
	seen := map[string]bool{}
	add := func(host, step string, needs ...Capability) {
		key := fmt.Sprintf("%s\x00%s\x00%v", host, step, needs)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, HostRequirement{Host: host, Step: step, AnyOf: slices.Clone(needs)})
	}

	for _, host := range slices.Sorted(maps.Keys(p.Hosts)) {
		for _, need := range p.Requires {
			add(host, "", need)
		}
		if len(p.Hosts[host].Media) > 0 {
			add(host, "declared host media", NeedsMedia, NeedsOneShot)
		}
	}

	for _, step := range p.Steps {
		switch {
		case step.Carry != nil:
			add(step.Host, step.Label, NeedsSession)
			add(step.Carry.ToHost, step.Label, NeedsCarry, NeedsSession)
		case IsDrive(step.Probe):
			add(step.Host, step.Label, NeedsEndpoint)
		default:
			add(step.Host, step.Label, NeedsSession, NeedsOneShot)
		}
	}

	return out
}
