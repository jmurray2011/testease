// Package statefile reads what somebody else provisioned.
//
// testease consumes targets and does not provision them, and this is where that
// boundary is drawn: an external tool brings an environment up, writes down what
// it made, and testease reads that file. What it can do with the result is
// whatever the file says the machine supports - which is the honest answer,
// because the provisioner knows and testease does not.
//
// The boundary is permanent. A provisioning platform belongs on the far side of
// this interface, and the day testease grows one is the day it stops being a
// thing that produces evidence and starts being a thing that owns machines.
package statefile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	ssmbackend "github.com/jmurray2011/testease/internal/backend/ssm"
	"github.com/jmurray2011/testease/internal/target"
)

// State is what an external provisioner wrote down.
//
// The field names are the provisioner's, not testease's. Renaming them here
// would put a translation layer between the file and the reader for no gain,
// and the first time the two drifted the record would say something the
// provisioner never said.
type State struct {
	Name       string `json:"name"`
	EnvID      string `json:"env_id"`
	InstanceID string `json:"instance_id"`
	LocalPort  int    `json:"local_port"`
	ForwardPID int    `json:"forward_pid"`
	Blueprint  string `json:"blueprint"`
	Product    string `json:"product"`
	Version    string `json:"version"`
	Origin     string `json:"origin"`
}

// Options configures the provider.
type Options struct {
	// Dir is where the provisioner keeps its state files.
	Dir string

	// SSM is how an instance named in a state file is reached. A state file
	// says which machine; it never says how to authenticate to it.
	Profile string
	Region  string

	RunID  string
	Runner string
	Log    *slog.Logger
}

// Provider resolves targets somebody else brought up.
type Provider struct{ opt Options }

var _ target.Provider = (*Provider)(nil)

// New builds a provider.
func New(opt Options) *Provider {
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	return &Provider{opt: opt}
}

// Resolve reads the state file a host names and dials what it describes.
func (p *Provider) Resolve(ctx context.Context, spec target.Spec) (target.Target, error) {
	name := spec.Address
	if name == "" {
		name = spec.Host
	}
	path := filepath.Join(p.opt.Dir, name+".json")

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("host %q reads its state from %s: %w", spec.Host, path, err)
	}
	var state State
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if state.InstanceID == "" {
		return nil, fmt.Errorf("%s names no instance, so there is nothing to dial", path)
	}

	client := ssmbackend.Client{Profile: p.opt.Profile, Region: p.opt.Region, Log: p.opt.Log}
	instance, err := client.Describe(ctx, state.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("host %q: %w", spec.Host, err)
	}

	m := &machine{
		state:   state,
		session: &ssmbackend.Session{Client: client, Instance: instance},
		labels:  p.labels(spec, state),
		identity: target.Identity{
			Requested: map[string]any{
				"provider":    "statefile",
				"state":       path,
				"blueprint":   state.Blueprint,
				"environment": state.Name,
			},
			Resolved: map[string]any{
				"backend": "ssm",
				"ref":     "instance:" + instance.ID,
				"env_id":  state.EnvID,
				"region":  p.opt.Region,
			},
			// What the provisioner says it built, kept apart from what the box
			// says about itself: one is a claim and the other is a reading.
			Facts: map[string]string{
				"os":                  instance.Platform,
				"platform":            instance.Name,
				"provisioner.product": state.Product,
				"provisioner.version": state.Version,
			},
		},
	}

	if state.Origin != "" {
		if u, err := url.Parse(state.Origin); err == nil && u.Host != "" {
			m.endpoint = u
		}
	}
	if m.endpoint == nil && state.LocalPort > 0 {
		// A forwarded port is reachable here rather than there, which is the
		// point of the forward.
		m.endpoint = &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(state.LocalPort))}
	}
	if m.endpoint != nil {
		m.identity.Resolved["endpoint"] = m.endpoint.String()
		return &endpointed{machine: m}, nil
	}
	return m, nil
}

// Release lets go of nothing. Whoever brought the environment up takes it down.
func (p *Provider) Release(context.Context, target.Target, bool) error { return nil }

func (p *Provider) labels(spec target.Spec, state State) map[string]string {
	return map[string]string{
		target.LabelRunner:  p.opt.Runner,
		target.LabelRun:     p.opt.RunID,
		target.LabelPID:     fmt.Sprint(os.Getpid()),
		target.LabelCreated: time.Now().UTC().Format(time.RFC3339),
		target.LabelBackend: "ssm",
		target.LabelHost:    spec.Host,
		target.LabelCell:    spec.Cell,
		"testease.env":      state.Name,
	}
}

type machine struct {
	state    State
	identity target.Identity
	labels   map[string]string
	session  target.Session
	endpoint *url.URL
}

var (
	_ target.Target    = (*machine)(nil)
	_ target.Sessioner = (*machine)(nil)
)

func (m *machine) Identity() target.Identity { return m.identity }

// Labels are recorded and reap never acts on them: testease created nothing
// here, so nothing here is testease's to remove.
func (m *machine) Labels() map[string]string { return m.labels }

func (m *machine) Session(context.Context) (target.Session, error) { return m.session, nil }

// endpointed is a machine the provisioner also said how to reach.
type endpointed struct{ *machine }

var _ target.Endpointer = (*endpointed)(nil)

func (e *endpointed) Endpoint() (url.URL, bool) { return *e.endpoint, true }
