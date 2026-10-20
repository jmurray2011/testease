package provisioned

import (
	"context"
	"testing"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

type fakePlatform struct {
	created Request
	waited  time.Duration
	destroyed string
}

func (f *fakePlatform) Create(_ context.Context, req Request) (Machine, error) {
	f.created = req
	return Machine{ID: "machine-fictional", Address: "machine-fictional", Image: req.Image, Labels: req.Labels}, nil
}
func (f *fakePlatform) WaitDialable(_ context.Context, _ Machine, budget time.Duration) error {
	f.waited = budget
	return nil
}
func (f *fakePlatform) List(context.Context, map[string]string) ([]Machine, error) { return nil, nil }
func (f *fakePlatform) Destroy(_ context.Context, m Machine) error { f.destroyed = m.ID; return nil }

func TestProvisionedProviderOwnsSharedLifecycle(t *testing.T) {
	t.Parallel()
	driver := &fakePlatform{}
	p := New(Options{Driver: driver, Name: "ec2", RunID: "run-fictional", Runner: "operator@workstation",
		Dial: func(context.Context, Machine) (target.Session, error) { return nil, nil }})
	tgt, err := p.Resolve(context.Background(), target.Spec{
		Host: "box", Provider: "ec2", Image: "ami-fictional", BootTimeout: 5 * time.Minute,
		Provision: map[string]string{"ec2.instance_type": "t3.small"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if driver.waited != 0 {
		t.Fatal("Resolve waited for dialability before returning the created machine")
	}
	if driver.created.Labels[target.LabelRun] != "run-fictional" || driver.created.Provision["ec2.instance_type"] != "t3.small" {
		t.Errorf("create request = %+v", driver.created)
	}
	if tgt.Identity().Resolved["ref"] != "ec2:machine-fictional" {
		t.Errorf("resolved identity = %+v", tgt.Identity())
	}
	if _, err := tgt.(target.Sessioner).Session(context.Background()); err != nil {
		t.Fatal(err)
	}
	if driver.waited != 5*time.Minute {
		t.Errorf("dialability budget = %s", driver.waited)
	}
	if err := p.Release(context.Background(), tgt, true); err != nil {
		t.Fatal(err)
	}
	if driver.destroyed != "" {
		t.Error("kept machine was destroyed")
	}
	if err := p.Release(context.Background(), tgt, false); err != nil {
		t.Fatal(err)
	}
	if driver.destroyed != "machine-fictional" {
		t.Errorf("destroyed = %q", driver.destroyed)
	}
}
