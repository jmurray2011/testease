// Package provisioned owns the lifecycle common to platform-created targets.
// A platform driver supplies native create, readiness, list, and destroy calls.
package provisioned

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"strings"
	"sync"
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

// OneShotDriver runs a command as a new native task. The callback must be
// invoked with its native ID before waiting for completion.
type OneShotDriver interface {
	Driver
	RunOnce(context.Context, Machine, []byte, []string, target.RunOpts, func(Machine) error) (target.Result, error)
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
	created := &createdTarget{provider: p, spec: spec, machine: machine}
	if driver, ok := p.opt.Driver.(OneShotDriver); ok {
		return &oneShotTarget{created: created, driver: driver}, nil
	}
	return created, nil
}

func (p *Provider) Release(ctx context.Context, t target.Target, keep bool) error {
	var created *createdTarget
	switch typed := t.(type) {
	case *createdTarget:
		created = typed
	case *oneShotTarget:
		created = typed.created
	default:
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
	mu       sync.Mutex
	session  target.Session
}

var (
	_ target.Target      = (*createdTarget)(nil)
	_ target.Sessioner   = (*createdTarget)(nil)
	_ target.MediaStager = (*createdTarget)(nil)
)

func (t *createdTarget) Identity() target.Identity {
	return t.identityFor(t.machine)
}

func (t *createdTarget) identityFor(machine Machine) target.Identity {
	resolved := map[string]any{
		"backend": t.provider.opt.Name,
		"ref":     t.provider.opt.Name + ":" + machine.ID,
		"id":      machine.ID,
		"address": machine.Address,
		"image":   machine.Image,
	}
	for key, value := range machine.Resolved {
		resolved[key] = value
	}
	return target.Identity{
		Requested: map[string]any{"provider": t.provider.opt.Name, "image": t.spec.Image},
		Resolved:  resolved,
		Facts:     map[string]string{},
	}
}

type oneShotTarget struct {
	created   *createdTarget
	driver    OneShotDriver
	onCreated func(target.Identity) error
}

var (
	_ target.Target           = (*oneShotTarget)(nil)
	_ target.OneShotter       = (*oneShotTarget)(nil)
	_ target.CreationReporter = (*oneShotTarget)(nil)
)

func (t *oneShotTarget) Identity() target.Identity { return t.created.Identity() }

func (t *oneShotTarget) Labels() map[string]string { return t.created.Labels() }

func (t *oneShotTarget) SetOnCreated(callback func(target.Identity) error) { t.onCreated = callback }

func (t *oneShotTarget) RunOnce(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	return t.driver.RunOnce(ctx, t.created.machine, script, args, opt, func(task Machine) error {
		if t.onCreated == nil {
			return fmt.Errorf("one-shot task %s has no creation recorder", task.ID)
		}
		return t.onCreated(t.created.identityFor(task))
	})
}

func (t *createdTarget) Labels() map[string]string { return maps.Clone(t.machine.Labels) }

func (t *createdTarget) Session(ctx context.Context) (target.Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session != nil {
		return t.session, nil
	}
	budget := max(MinWaitBudget, t.spec.BootTimeout)
	if err := t.provider.opt.Driver.WaitDialable(ctx, t.machine, budget); err != nil {
		return nil, err
	}
	if t.provider.opt.Dial == nil {
		return nil, fmt.Errorf("%s has no session transport", t.provider.opt.Name)
	}
	session, err := t.provider.opt.Dial(ctx, t.machine)
	if err != nil {
		return nil, err
	}
	t.session = session
	return session, nil
}

// Stage copies media through the session and reads it back across the target
// boundary. A named input must still have the digest the caller declared.
func (t *createdTarget) Stage(ctx context.Context, items []target.Media) error {
	session, err := t.Session(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Source == "" || item.Dest == "" {
			return fmt.Errorf("media needs both source and destination")
		}
		info, err := os.Stat(item.Source)
		if err != nil {
			return fmt.Errorf("stat media %s: %w", item.Source, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("media %s is not a regular file", item.Source)
		}
		file, err := os.Open(item.Source)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		if item.ReadOnly {
			mode &= 0o555
		}
		putErr := session.Put(ctx, file, item.Dest, mode)
		closeErr := file.Close()
		if putErr != nil {
			return fmt.Errorf("stage media %s: %w", item.Source, putErr)
		}
		if closeErr != nil {
			return closeErr
		}
		local, err := os.Open(item.Source)
		if err != nil {
			return err
		}
		localHash := sha256.New()
		_, hashErr := io.Copy(localHash, local)
		_ = local.Close()
		if hashErr != nil {
			return hashErr
		}
		want := hex.EncodeToString(localHash.Sum(nil))
		if declared := strings.TrimPrefix(strings.ToLower(item.SHA256), "sha256:"); declared != "" && declared != want {
			return fmt.Errorf("local media %s changed since input resolution", item.Source)
		}
		remote, err := session.Fetch(ctx, item.Dest)
		if err != nil {
			return fmt.Errorf("read staged media %s: %w", item.Dest, err)
		}
		remoteHash := sha256.New()
		_, hashErr = io.Copy(remoteHash, remote)
		closeErr = remote.Close()
		if hashErr != nil {
			return hashErr
		}
		if closeErr != nil {
			return closeErr
		}
		if got := hex.EncodeToString(remoteHash.Sum(nil)); got != want {
			return fmt.Errorf("staged media %s differs from source (sha256 %s, want %s)", item.Dest, got, want)
		}
	}
	return nil
}
