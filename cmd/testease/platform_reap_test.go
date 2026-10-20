package main

import (
	"path/filepath"
	"testing"

	"github.com/jmurray2011/testease/internal/provider/provisioned"
	"github.com/jmurray2011/testease/internal/reap"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/target"
)

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
