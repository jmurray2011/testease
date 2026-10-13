package runner

import (
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/scenario"
)

func scenarioRef(name string, identity scenario.ContentIdentity) record.ScenarioRef {
	return record.ScenarioRef{
		Name:        name,
		Hash:        identity.Hash,
		HashVersion: identity.HashVersion,
		Probes:      identity.Probes,
		TreeHash:    identity.TreeHash,
		Files:       identity.Files,
	}
}
