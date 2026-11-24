package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/scenario"
)

func TestCloudSmokeScenariosResolveWithoutAccountSettings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		provider string
		params   map[string]string
	}{
		{
			name:     "ec2-media",
			provider: "ec2",
			params:   map[string]string{"image": "ami-example", "payload": "/tmp/payload.bin"},
		},
		{
			name:     "ecs-artifact",
			provider: "ecs",
			params:   map[string]string{"image": "example.invalid/repository:smoke"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m, err := scenario.Load(filepath.Join("..", "..", "testdata", "cloud-smoke", tc.name))
			if err != nil {
				t.Fatalf("load cloud smoke scenario: %v", err)
			}
			plan, err := m.Resolve(tc.params)
			if err != nil {
				t.Fatalf("resolve cloud smoke scenario: %v", err)
			}
			if plan.Hosts["box"].Provider != tc.provider {
				t.Errorf("provider = %q, want %q", plan.Hosts["box"].Provider, tc.provider)
			}
			if plan.Judge == nil || plan.Judge.Name != "expect" || len(plan.Artifacts) != 1 || !plan.Artifacts[0].Required {
				t.Errorf("scenario must judge the probe and require its returned artifact: judge=%+v artifacts=%+v", plan.Judge, plan.Artifacts)
			}
			for _, step := range plan.Steps {
				probe := filepath.Join(plan.Dir, step.Probe)
				info, err := os.Stat(probe)
				if err != nil {
					t.Errorf("probe %s: %v", step.Probe, err)
				} else if !shippedExecutable(t, probe, info) {
					t.Errorf("probe %s is not executable", step.Probe)
				}
			}
		})
	}
}
