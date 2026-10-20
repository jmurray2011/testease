package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/reap"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/target"
)

type fakePlatformInventory struct {
	machines []provisioned.Machine
	destroyed []string
}

func (f *fakePlatformInventory) List(context.Context, map[string]string) ([]provisioned.Machine, error) {
	return f.machines, nil
}
func (f *fakePlatformInventory) Destroy(_ context.Context, machine provisioned.Machine) error {
	f.destroyed = append(f.destroyed, machine.ID)
	return nil
}

func TestPlatformReapForcesOnlyProvedRelease(t *testing.T) {
	t.Parallel()
	runsDir := t.TempDir()
	w, err := record.Open(filepath.Join(runsDir, "run-fictional"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: "run-fictional"})
	for _, item := range []struct{ host, id, action string }{
		{"release-box", "i-release", "release-intended"},
		{"kept-box", "i-kept", "retained"},
	} {
		_, _ = w.Emit(record.KindTargetResolved, 0, record.TargetResolved{Host: item.host,
			Resolved: map[string]any{"ref": "ec2:" + item.id}})
		_, _ = w.Emit(record.KindTargetDisposition, 0, record.TargetDisposition{
			Host: item.host, Ref: "ec2:" + item.id, Action: item.action})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	inventory := &fakePlatformInventory{}
	for _, item := range []struct{ host, id string }{{"release-box", "i-release"}, {"kept-box", "i-kept"}} {
		inventory.machines = append(inventory.machines, provisioned.Machine{ID: item.id,
			Labels: map[string]string{target.LabelRun: "run-fictional", target.LabelHost: item.host,
				target.LabelRunner: "operator@workstation"}})
	}
	report := platformReap(context.Background(), inventory, "ec2", runsDir, "operator@workstation", true)
	if report.Removable != 1 || len(report.Removed) != 1 || report.Removed[0] != "i-release" {
		t.Errorf("report = %+v", report)
	}
	if len(inventory.destroyed) != 1 || inventory.destroyed[0] != "i-release" {
		t.Errorf("destroyed = %v", inventory.destroyed)
	}
}

func TestReapAcceptsAnExplicitECSCluster(t *testing.T) {
	t.Parallel()
	cmd := reapCmd()
	if err := cmd.ParseFlags([]string{"--platform", "ecs", "--ecs-cluster", "fictional-cluster",
		"--aws-profile", "fictional-profile", "--aws-region", "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	if got := flagString(cmd, "ecs-cluster"); got != "fictional-cluster" {
		t.Errorf("ECS cluster = %q", got)
	}
}

func TestPlatformReapRequiresMatchingReleaseIntent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		action string
		id     string
		want   reap.Fate
	}{
		{name: "release intended", action: "release-intended", id: "i-fictional", want: reap.Orphaned},
		{name: "retained", action: "retained", id: "i-fictional", want: reap.Unprovable},
		{name: "different instance", action: "release-intended", id: "i-stranger", want: reap.Unprovable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runsDir := t.TempDir()
			w, err := record.Open(filepath.Join(runsDir, "run-fictional"))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Emit(record.KindRunStarted, 0, record.RunStarted{RunID: "run-fictional"})
			_, _ = w.Emit(record.KindTargetResolved, 0, record.TargetResolved{Host: "box",
				Resolved: map[string]any{"ref": "ec2:i-fictional"}})
			_, _ = w.Emit(record.KindTargetDisposition, 0, record.TargetDisposition{
				Host: "box", Ref: "ec2:i-fictional", Action: tc.action})
			_, _ = w.Emit(record.KindRunFinished, 0, record.RunFinished{Status: record.StatusMeasured})
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			machine := provisioned.Machine{ID: tc.id, Labels: map[string]string{
				target.LabelRun: "run-fictional", target.LabelHost: "box",
				target.LabelRunner: "operator@workstation",
			}}
			got, _ := platformFate(runsDir, "ec2", machine)
			if got != tc.want {
				t.Errorf("fate = %s, want %s", got, tc.want)
			}
		})
	}
}
