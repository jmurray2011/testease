package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmurray2011/testease/internal/doctor"
)

type fakePlatformCheck struct {
	images []string
	err    error
}

func (f *fakePlatformCheck) Check(_ context.Context, image string, _ map[string]string) error {
	f.images = append(f.images, image)
	return f.err
}

func TestDoctorChecksOnlyPlatformsInResolvedScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "cloud")
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(`schema: 1
name: cloud
question: can the box run?
hosts:
  box: {provider: ec2, image: ami-fictional}
steps:
  - {label: run, host: box, probe: probes/one.sh}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probes/one.sh"), []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ec2 := &fakePlatformCheck{}
	ecs := &fakePlatformCheck{}
	report := doctor.Run(context.Background(), doctor.Options{
		RunsDir: t.TempDir(), ScenarioRoot: root, Scenario: "cloud",
		Provision: map[string]string{"ec2.subnet_id": "subnet-fictional"},
		Platforms: map[string]doctor.Platform{"ec2": ec2, "ecs": ecs},
	})
	if len(ec2.images) != 1 || ec2.images[0] != "ami-fictional" || len(ecs.images) != 0 {
		t.Errorf("checked ec2=%v ecs=%v", ec2.images, ecs.images)
	}
	if c := check(t, report, "platform:ec2"); c.Status != doctor.OK {
		t.Errorf("EC2 check = %+v", c)
	}
	for _, c := range report.Checks {
		if strings.HasPrefix(c.Name, "platform:ecs") {
			t.Errorf("unselected ECS was checked: %+v", c)
		}
	}
}
