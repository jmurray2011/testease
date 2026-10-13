// Package hyperv hands back targets that run one command and exit.
//
// It creates nothing until a probe runs: there is no box standing between
// steps, because a one-shot cell IS the step. What the provider resolves is
// the promise of one - an image that answers, on a daemon that can run it.
package hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	oneshot "github.com/jmurray2011/testease/internal/backend/hyperv"
	"github.com/jmurray2011/testease/internal/target"
)

// Options configures the provider for one run.
type Options struct {
	Client backend.Client

	RunID  string
	Runner string

	// ScratchRoot is the run-owned runtime directory inspected by `testease
	// reap`. Carried bytes live in a private child so a killed runner leaves
	// attributable, recoverable scratch rather than anonymous /tmp files.
	ScratchRoot string

	// Memory is what each cell gets. Windows containers are not small.
	Memory string

	Log *slog.Logger
}

// Provider resolves one-shot targets.
type Provider struct{ opt Options }

var _ target.Provider = (*Provider)(nil)

// New builds a provider.
func New(opt Options) *Provider {
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	opt.Client.Log = opt.Log
	return &Provider{opt: opt}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// Resolve checks that the image is there and hands back something that can run
// one command in it.
//
// Nothing is created here. A one-shot target has no life between steps, so
// resolving it means confirming it could exist rather than making it exist -
// and the record says exactly that, rather than implying a box that is
// standing somewhere.
func (p *Provider) Resolve(ctx context.Context, spec target.Spec) (target.Target, error) {
	if spec.Image == "" {
		return nil, fmt.Errorf("a one-shot host needs an image")
	}

	id := target.Identity{
		Requested: map[string]any{"provider": "hyperv", "image": spec.Image},
		Resolved:  map[string]any{"backend": "one-shot", "ref": "image:" + spec.Image},
		Facts:     map[string]string{},
	}

	digest, err := p.opt.Client.Output(ctx, "image", "inspect", "-f", "{{.Id}}", spec.Image)
	if err != nil {
		// The failure is recorded in the layer that failed rather than the
		// event being left out: a gap must never read as an omission.
		id.Resolved["image_digest_error"] = err.Error()
		id.Facts["error"] = "the image could not be inspected, so nothing was read off it"
		return nil, fmt.Errorf("host %q cannot run %s: %w", spec.Host, spec.Image, err)
	}
	id.Resolved["image_digest"] = digest

	if osType, err := p.opt.Client.Output(ctx, "version", "--format", "{{.Server.Os}}"); err == nil {
		id.Facts["daemon_os"] = osType
	}

	return &cell{provider: p, spec: spec, identity: id, labels: p.labels(spec)}, nil
}

// Release removes runner-owned carry scratch. Each container already removes
// itself when its command ends, because the container is the command.
func (p *Provider) Release(_ context.Context, t target.Target, _ bool) error {
	c, ok := t.(*cell)
	if !ok {
		return nil
	}
	if c.scratchDir == "" {
		return nil
	}
	if err := os.RemoveAll(c.scratchDir); err != nil {
		return err
	}
	// Remove only empty parents. A sibling cell or a secret workspace keeps
	// either directory non-empty and therefore safely outlives this release.
	_ = os.Remove(p.opt.ScratchRoot)
	_ = os.Remove(filepath.Dir(p.opt.ScratchRoot))
	return nil
}

func (p *Provider) labels(spec target.Spec) map[string]string {
	return map[string]string{
		target.LabelRunner:  p.opt.Runner,
		target.LabelRun:     p.opt.RunID,
		target.LabelPID:     fmt.Sprint(os.Getpid()),
		target.LabelCreated: time.Now().UTC().Format(time.RFC3339),
		target.LabelBackend: "one-shot",
		target.LabelHost:    spec.Host,
		target.LabelCell:    spec.Cell,
	}
}

// cell is a target that runs one command at a time, each in its own container.
type cell struct {
	provider   *Provider
	spec       target.Spec
	identity   target.Identity
	labels     map[string]string
	seq        int
	carried    []target.Media
	scratchDir string
}

var (
	_ target.Target        = (*cell)(nil)
	_ target.OneShotter    = (*cell)(nil)
	_ target.CarryReceiver = (*cell)(nil)
)

func (c *cell) Identity() target.Identity { return c.identity }
func (c *cell) Labels() map[string]string { return c.labels }

// ReceiveCarry owns a local copy until the cell's next one-shot execution.
// RunOnce hands it to the backend as media, whose copy window is after create
// and before start.
func (c *cell) ReceiveCarry(_ context.Context, body io.Reader, remote string) (target.CarryReceipt, error) {
	if err := c.prepareCarryScratch(); err != nil {
		return target.CarryReceipt{}, err
	}
	local, err := os.CreateTemp(c.scratchDir, "payload-")
	if err != nil {
		return target.CarryReceipt{}, fmt.Errorf("retain carry for %s: %w", remote, err)
	}
	name := local.Name()
	ok := false
	defer func() {
		_ = local.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if _, err := io.Copy(local, body); err != nil {
		return target.CarryReceipt{}, fmt.Errorf("retain carry for %s: %w", remote, err)
	}
	if err := local.Close(); err != nil {
		return target.CarryReceipt{}, fmt.Errorf("close carry for %s: %w", remote, err)
	}
	ok = true
	c.carried = append(c.carried, target.Media{Source: name, Dest: remote})
	return target.CarryReceipt{SeededBeforeStart: true}, nil
}

func (c *cell) prepareCarryScratch() error {
	if c.scratchDir != "" {
		return nil
	}
	if c.provider.opt.ScratchRoot == "" {
		return fmt.Errorf("retain carry: no run-owned scratch root was configured")
	}
	if err := os.MkdirAll(c.provider.opt.ScratchRoot, 0o700); err != nil {
		return fmt.Errorf("make room for carry scratch: %w", err)
	}
	dir, err := os.MkdirTemp(c.provider.opt.ScratchRoot, "carry-")
	if err != nil {
		return fmt.Errorf("create carry scratch: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()

	host, _ := os.Hostname()
	owner, err := json.Marshal(struct {
		RunID string `json:"run_id"`
		Cell  string `json:"cell,omitempty"`
		User  string `json:"user"`
		Host  string `json:"host"`
		PID   int    `json:"pid"`
	}{c.provider.opt.RunID, c.spec.Cell, os.Getenv("USER"), host, os.Getpid()})
	if err != nil {
		return fmt.Errorf("describe carry scratch owner: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), append(owner, '\n'), 0o600); err != nil {
		return fmt.Errorf("write carry scratch owner: %w", err)
	}
	c.scratchDir = dir
	ok = true
	return nil
}

func (c *cell) stagedMedia() []oneshot.Stage {
	items := make([]target.Media, 0, len(c.spec.Media)+len(c.carried))
	items = append(items, c.spec.Media...)
	items = append(items, c.carried...)
	return media(items)
}

// RunOnce ships the script in and runs it as the container's whole command.
//
// The script is staged while the container is merely created, because docker cp
// is refused once a Hyper-V container is running. Readings come back the same
// way they went in - as a file, taken out after the command ends - and are
// handed to the caller on the structured channel, the same one a probe writes
// to on fd 3 where a shell can offer one.
func (c *cell) RunOnce(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	c.seq++
	name := containerName(c.provider.opt.RunID, c.spec.Cell, c.spec.Host, c.seq)
	carried := c.stagedMedia()
	c.carried = nil

	shot := oneshot.OneShot{
		Client: c.provider.opt.Client,
		OS:     c.identity.Facts["daemon_os"],
		Name:   name,
		Image:  c.spec.Image,
		Labels: c.labels,
		Memory: c.provider.opt.Memory,
		Media:  carried,
	}
	return shot.Run(ctx, script, args, opt)
}

func containerName(runID, cell, host string, seq int) string {
	parts := []string{"testease", runID, cell, host, fmt.Sprint(seq)}
	name := unsafeName.ReplaceAllString(strings.Join(parts, "-"), "-")
	if len(name) > 60 {
		// Keep the tail: the cell, host and sequence are what tell two live
		// containers apart.
		name = name[len(name)-60:]
	}
	return name
}

// media is what the host declared, in the shape the one-shot backend stages.
func media(items []target.Media) []oneshot.Stage {
	out := make([]oneshot.Stage, 0, len(items))
	for _, m := range items {
		out = append(out, oneshot.Stage{Local: m.Source, Remote: m.Dest})
	}
	return out
}
