// Package provisioned owns the lifecycle common to platform-created targets.
// A platform driver supplies native create, readiness, list, and destroy calls.
package provisioned

import (
	"context"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

const MinWaitBudget = 3 * time.Minute

// Request is one platform creation with native ownership labels.
type Request struct {
	Image     string
	Provision map[string]string
	Labels    map[string]string
}

// Machine identifies a created platform resource without assuming a transport.
type Machine struct {
	ID       string
	Address  string
	Image    string
	Resolved map[string]any
	Labels   map[string]string
}

// Driver is the platform-specific lifecycle. Its methods must be safe to
// retry against a resource already being removed.
type Driver interface {
	Create(context.Context, Request) (Machine, error)
	WaitDialable(context.Context, Machine, time.Duration) error
	List(context.Context, map[string]string) ([]Machine, error)
	Destroy(context.Context, Machine) error
}

// Options binds one platform driver to a run. Dial is the transport boundary:
// an EC2 driver creates an instance while the SSM backend opens the session.
type Options struct {
	Driver Driver
	Name   string
	RunID  string
	Runner string
	Dial   func(context.Context, Machine) (target.Session, error)
}

type Provider struct{ opt Options }

var _ target.Provider = (*Provider)(nil)

func New(opt Options) *Provider { return &Provider{opt: opt} }

func (p *Provider) Resolve(ctx context.Context, spec target.Spec) (target.Target, error) {
	if spec.Image == "" {
		return nil, fmt.Errorf("host %q needs an image for %s", spec.Host, p.opt.Name)
	}
	labels := map[string]string{
		target.LabelRunner:  p.opt.Runner,
		target.LabelRun:     p.opt.RunID,
		target.LabelPID:     fmt.Sprint(os.Getpid()),
		target.LabelCreated: time.Now().UTC().Format(time.RFC3339),
		target.LabelBackend: p.opt.Name,
		target.LabelHost:    spec.Host,
		target.LabelCell:    spec.Cell,
	}
	machine, err := p.opt.Driver.Create(ctx, Request{
		Image: spec.Image, Provision: maps.Clone(spec.Provision), Labels: maps.Clone(labels),
	})
	if err != nil {
		return nil, err
	}
	if machine.ID == "" {
		return nil, fmt.Errorf("%s created host %q without a resource id", p.opt.Name, spec.Host)
	}
	if machine.Address == "" {
		machine.Address = machine.ID
	}
	if len(machine.Labels) == 0 {
		machine.Labels = labels
	}
	return &createdTarget{provider: p, spec: spec, machine: machine}, nil
}

func (p *Provider) Release(ctx context.Context, t target.Target, keep bool) error {
	created, ok := t.(*createdTarget)
	if !ok {
		return fmt.Errorf("%s cannot release a target it did not create", p.opt.Name)
	}
	if keep {
		return nil
	}
	return p.opt.Driver.Destroy(ctx, created.machine)
}

type createdTarget struct {
	provider *Provider
	spec     target.Spec
	machine  Machine
}

var (
	_ target.Target    = (*createdTarget)(nil)
	_ target.Sessioner = (*createdTarget)(nil)
)

func (t *createdTarget) Identity() target.Identity {
	resolved := map[string]any{
		"backend": t.provider.opt.Name,
		"ref":     t.provider.opt.Name + ":" + t.machine.ID,
		"id":      t.machine.ID,
		"address": t.machine.Address,
		"image":   t.machine.Image,
	}
	for key, value := range t.machine.Resolved {
		resolved[key] = value
	}
	return target.Identity{
		Requested: map[string]any{"provider": t.provider.opt.Name, "image": t.spec.Image},
		Resolved:  resolved,
		Facts:     map[string]string{},
	}
}

func (t *createdTarget) Labels() map[string]string { return maps.Clone(t.machine.Labels) }

func (t *createdTarget) Session(ctx context.Context) (target.Session, error) {
	budget := max(MinWaitBudget, t.spec.BootTimeout)
	if err := t.provider.opt.Driver.WaitDialable(ctx, t.machine, budget); err != nil {
		return nil, err
	}
	if t.provider.opt.Dial == nil {
		return nil, fmt.Errorf("%s has no session transport", t.provider.opt.Name)
	}
	return t.provider.opt.Dial(ctx, t.machine)
}
