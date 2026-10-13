// Package docker boots a container and hands back a target to observe.
//
// It creates; it does not provision. Anything larger than "run this image and
// tell me what answered" belongs on the far side of the provider interface.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	backend "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/provider/fingerprint"
	"github.com/jmurray2011/testease/internal/target"
)

// BootBudget is how long a container has to become ready.
//
// 180 seconds, and the number is measured rather than chosen. The default was
// 60 and that was wrong four times over in one day: a baked target settled at
// 45s on an idle host, three runs in six failed inside 60s with the host under
// load, and an imported appliance took about 100s because it boots a full unit
// set. A short budget reports a boot failure on a container that is merely
// still starting, and the record then blames the image. The cost of the larger
// number is only ever paid by a target that genuinely cannot start.
const BootBudget = 180 * time.Second

// pollEvery is how often readiness is asked about. The budget is only spent in
// full when a boot really fails; the loop returns the moment the target
// answers.
const pollEvery = 2 * time.Second

// Instrument is an external fingerprinting tool, configured rather than
// compiled in. Collect runs on the target and prints one reading; Diff runs on
// this machine over two of those readings and exits 0 for no change, 1 for
// change, anything else for its own failure.
//
// testease never looks inside a reading or a diff. The instrument owns that
// format, and the diff is kept as opaque bytes until a judge has a reason to
// read it.
type Instrument = fingerprint.Instrument

// Options configures the provider for one run.
type Options struct {
	Client backend.Client

	// RunID and Runner go onto every container this creates, so that forensic
	// cleanup can answer "is this safe to remove" from labels alone rather
	// than from how old it looks.
	RunID  string
	Runner string

	// Instrument, when set, makes every target a Fingerprinter. Unset, a
	// target is honestly not one, and drift is recorded unavailable.
	Instrument *Instrument

	BootBudget time.Duration

	// Notices is where an operator instruction goes - the exact command that
	// clears up a target left standing. It is not gated by a log level:
	// something a person must act on has to be visible at any verbosity.
	Notices io.Writer

	// Log carries diagnostics about dialing: what was tried, how long a boot
	// took, why it did not answer. Nil says nothing.
	Log *slog.Logger
}

// Provider creates containers and hands back targets.
type Provider struct {
	opt Options

	// networks are the ones this provider created, one per cell. Cells run in
	// parallel, so creation is guarded.
	mu       sync.Mutex
	networks map[string]bool

	// daemonOnce reads the daemon's name once for the whole run. Every
	// container this provider makes is on the same daemon, and a census asks
	// for a hundred of them.
	daemonOnce sync.Once
	daemonName string
}

var _ target.Provider = (*Provider)(nil)

// New builds a provider. The caller closes nothing: Release cleans up what
// Resolve created, according to keep.
func New(opt Options) *Provider {
	if opt.BootBudget == 0 {
		opt.BootBudget = BootBudget
	}
	if opt.Notices == nil {
		opt.Notices = io.Discard
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	// One client, one place diagnostics come from.
	opt.Client.Log = opt.Log
	return &Provider{opt: opt}
}

// dockerContext is the context this provider dials through, named so that a
// reader can dial the same one - and so that reap can be pointed at it.
func (p *Provider) dockerContext() string {
	if p.opt.Client.Context == "" {
		return "(default)"
	}
	return p.opt.Client.Context
}

// daemon is what the daemon calls the machine it runs on, read once per run.
// A daemon that will not say is recorded as not having said, because a blank
// here would read as a local run.
func (p *Provider) daemon(ctx context.Context) string {
	p.daemonOnce.Do(func() {
		name, err := p.opt.Client.Output(ctx, "info", "--format", "{{.Name}}")
		if err != nil {
			p.daemonName = "unknown: " + err.Error()
			return
		}
		p.daemonName = strings.TrimSpace(name)
	})
	return p.daemonName
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// Resolve boots the container a host spec asks for and waits for it to answer.
func (p *Provider) Resolve(ctx context.Context, spec target.Spec) (target.Target, error) {
	if spec.Image == "" {
		return nil, errors.New("a docker host needs an image")
	}
	name := containerName(p.opt.RunID, spec.Cell, spec.Host)

	network, err := p.network(ctx, spec)
	if err != nil {
		return nil, err
	}

	// The alias is the name the scenario gave the host, so a probe reaches
	// another host by writing down what the manifest calls it. Nothing new to
	// template, and the address never has to leave the runner.
	// How a container of this kind is made, staged and started is the
	// backend's, chosen once. This asks for one and does not care what sort of
	// machine answers.
	log := p.opt.Log.With("host", spec.Host, "image", spec.Image, "container", name)
	if err := p.opt.Client.Up(ctx, backend.Launch{
		Name:    name,
		Image:   spec.Image,
		Network: network,
		Alias:   spec.Host,
		Labels:  p.labels(spec),
		Env:     spec.Env,
		Media:   spec.Media,
		Init:    len(spec.Ready) > 0,
	}); err != nil {
		log.Warn("target did not start", "error", err)
		return nil, fmt.Errorf("host %q did not start from %s: %w", spec.Host, spec.Image, err)
	}

	session := &backend.Session{Client: p.opt.Client, Container: name, OS: p.opt.Client.ServerOS(ctx)}
	if err := p.waitReady(ctx, session, spec); err != nil {
		return nil, err
	}

	c := &container{
		provider: p,
		name:     name,
		spec:     spec,
		session:  session,
		labels:   p.labels(spec),
		network:  network,
	}
	if p.opt.Instrument != nil {
		c.printer = fingerprint.New(session, *p.opt.Instrument, name)
	}
	c.identity = p.identify(ctx, c)
	log.Debug("target resolved", "digest", c.identity.Resolved["image_digest"], "facts", c.identity.Facts)

	var printer target.Fingerprinter
	if c.printer != nil {
		printer = c.printer
	}
	return p.withEndpoint(c, printer), nil
}

// Release removes the container, or says exactly how to remove it later.
func (p *Provider) Release(ctx context.Context, t target.Target, keep bool) error {
	c, ok := unwrap(t)
	if !ok {
		return nil
	}
	// The readings go whatever happens. They are working material, not
	// evidence: what a fingerprint concluded is an event, and the instrument's
	// own diff is already an artifact inside the record. Keeping them past the
	// run would leave unlabelled files behind that reap has no way to reason
	// about. THIS container's readings only: cells run in parallel, and the
	// first cell released must not destroy another's before-snapshot.
	c.printer.Close()

	if keep {
		// A half-mutated box answers questions the record does not, so the
		// measured default is to leave it - but never without saying what it
		// costs to clear up.
		_, _ = fmt.Fprintf(p.opt.Notices, "kept target %s; remove it with: docker rm -f %s\n", c.name, c.name)
		return nil
	}
	if _, err := p.opt.Client.Output(ctx, "rm", "-f", c.name); err != nil {
		return fmt.Errorf("remove target %s: %w", c.name, err)
	}

	// The network goes with the last host on it. Docker refuses to remove one
	// that still has containers attached, which is the check rather than a
	// count kept here: another host of the same cell releasing later would find
	// it gone otherwise.
	name := networkName(p.opt.RunID, c.spec.Cell)
	p.mu.Lock()
	created := p.networks[name]
	p.mu.Unlock()
	if created {
		if _, err := p.opt.Client.Output(ctx, "network", "rm", name); err == nil {
			p.mu.Lock()
			delete(p.networks, name)
			p.mu.Unlock()
		}
	}
	return nil
}

func (p *Provider) labels(spec target.Spec) map[string]string {
	return map[string]string{
		target.LabelRunner:  p.opt.Runner,
		target.LabelRun:     p.opt.RunID,
		target.LabelPID:     fmt.Sprint(os.Getpid()),
		target.LabelCreated: time.Now().UTC().Format(time.RFC3339),
		target.LabelBackend: "docker-exec",
		target.LabelHost:    spec.Host,
		target.LabelCell:    spec.Cell,
	}
}

// waitReady polls until the target says it has finished starting. What counts
// as ready is the scenario's to declare; how long to wait for it is not.
func (p *Provider) waitReady(ctx context.Context, session *backend.Session, spec target.Spec) error {
	probe := spec.Ready
	if len(probe) == 0 {
		// The weakest honest claim: the execution surface answers at all. What
		// that costs to ask is the backend's business.
		probe = p.opt.Client.ReadyProbe(ctx)
	}

	log := p.opt.Log.With("host", spec.Host, "container", session.Container, "ready", probe)
	budget := p.opt.BootBudget
	if spec.BootTimeout > budget {
		// Raised by the host, never lowered: the manifest refuses a tighter one
		// before it ever reaches here.
		budget = spec.BootTimeout
	}
	log.Debug("waiting for the target to answer", "budget", budget)

	started := time.Now()
	deadline := started.Add(budget)
	var last error
	for {
		// Each attempt is bounded on its own: a wedged daemon otherwise holds
		// one exec open forever, and the budget's deadline is only ever
		// checked between attempts.
		//
		// THE EFFECTIVE BOUND IS SIXTY SECONDS, not the thirty written here.
		// Session.exec adds its own +30s backstop outside whatever Timeout
		// says, deliberately, so that a probe's in-container `timeout` has
		// room to fire first. That headroom applies to this call too, so a
		// wedged daemon holds each attempt for 30+30 and the boot budget is
		// spent about three attempts at a time rather than six.
		result, err := session.Run(ctx, probe, target.RunOpts{
			Stdout: io.Discard, Stderr: io.Discard, Timeout: 30 * time.Second,
		})
		if err == nil && result.RC == 0 {
			// How long a boot really took is the number that corrects a wrong
			// budget. The 60-second default was wrong four times in a day and
			// nobody could see it, because nothing wrote this down.
			log.Debug("target answered", "waited", time.Since(started).Round(time.Second))
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			break
		}
		log.Debug("target not answering yet", "waited", time.Since(started).Round(time.Second), "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
	log.Warn("target never answered", "waited", time.Since(started).Round(time.Second), "error", last)
	if last == nil {
		// The probe ran and said no. That is a different thing from the
		// machine not answering at all, and the wording has to hold the
		// difference or a boot that is merely slow reads like a dead box.
		return fmt.Errorf("host %q answered %v within %s, and it never reported ready",
			spec.Host, probe, budget)
	}
	return fmt.Errorf("host %q never answered %v within %s: %w",
		spec.Host, probe, budget, last)
}

// identify reads the three layers apart from each other: what was asked for,
// what actually answered, and what the box says about itself.
func (p *Provider) identify(ctx context.Context, c *container) target.Identity {
	id := target.Identity{
		Requested: map[string]any{
			"provider": "docker",
			"image":    c.spec.Image,
		},
		Resolved: map[string]any{
			"backend": "docker-exec",
			"ref":     "container:" + c.name,
			// WHICH MACHINE HELD THE BOX. host.hostname in run-started is the
			// runner's, so without these a record of a run against
			// --docker-context winhost read exactly like a local one, and the
			// only trace was an inference from a kernel fact. The context is
			// recorded even when it is the default, because an absent key
			// means nobody wrote it down rather than that there was none.
			"docker_context": p.dockerContext(),
			"daemon":         p.daemon(ctx),
		},
		Facts: map[string]string{},
	}

	// The image digest, not the tag: a tag is a name someone can move, and a
	// record that cites one cannot say what it measured.
	if digest, err := p.opt.Client.Output(ctx, "inspect", "-f", "{{.Image}}", c.name); err == nil {
		id.Resolved["image_digest"] = digest
	} else {
		id.Resolved["image_digest_error"] = err.Error()
	}
	if addr, err := p.opt.Client.Output(ctx, "inspect", "-f",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", c.name); err == nil && addr != "" {
		id.Resolved["address"] = addr
	}

	probe := identityProbe(c.name, c.session.OS)
	facts, err := p.opt.Client.Output(ctx, probe...)
	if err != nil {
		id.Facts["error"] = err.Error()
		return id
	}
	lines := strings.Split(facts, "\n")
	for i, key := range []string{"kernel_name", "arch", "kernel", "os"} {
		if i < len(lines) {
			id.Facts[key] = strings.TrimSpace(lines[i])
		}
	}
	return id
}

func identityProbe(container, osName string) []string {
	probe := []string{
		"exec", container, "sh", "-c",
		`uname -s; uname -m; uname -r; (. /etc/os-release 2>/dev/null && echo "$ID-$VERSION_ID") || echo unknown`,
	}
	if strings.EqualFold(osName, backend.Windows) {
		// Windows containers have neither sh nor uname. Asking them through
		// the Unix probe records an identity error on an otherwise resolved
		// target, which makes a complete Windows run look partly unidentified.
		probe = []string{
			"exec", container, "powershell", "-NoProfile", "-NonInteractive", "-Command",
			`$os = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'; ` +
				`'Windows_NT'; $env:PROCESSOR_ARCHITECTURE; ` +
				`[Environment]::OSVersion.Version.ToString(); ` +
				`if ($os.ProductName) { $os.ProductName } else { 'windows' }`,
		}
	}
	return probe
}

// containerName has to be unique per target, and a census runs many cells of
// one run at once - so the cell belongs in the name. Without it every cell of a
// census would ask for the same container, and the second would either collide
// or, worse, measure the first one's machine.
//
// The run id is trimmed rather than the cell or host when the name is too long,
// because those are what tell two live targets apart. Ownership stays readable
// from the labels either way, which is what reap trusts.
// network is the cell's own network, created on first use. Hosts on it resolve
// each other by the name the scenario gave them.
func (p *Provider) network(ctx context.Context, spec target.Spec) (string, error) {
	name := networkName(p.opt.RunID, spec.Cell)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.networks[name] {
		return name, nil
	}
	if err := p.opt.Client.EnsureNetwork(ctx, name, p.labels(spec)); err != nil {
		return "", fmt.Errorf("create network %s: %w", name, err)
	}
	if p.networks == nil {
		p.networks = map[string]bool{}
	}
	p.networks[name] = true
	return name, nil
}

// networkName is the network a cell's hosts share, and only that cell's.
//
// Per cell, not per run: hosts are reachable by the name the scenario gave
// them, so every cell of a census aliases the same host names. On one network
// docker would accept the duplicates and answer either, and a probe would reach
// another cell's machine with nothing reporting an error.
func networkName(runID, cell string) string {
	tail := unsafeName.ReplaceAllString(cell, "-")
	prefix := unsafeName.ReplaceAllString("testease-net-"+runID, "-")

	const limit = 60
	if tail == "" {
		if len(prefix) > limit {
			return prefix[:limit]
		}
		return prefix
	}
	// The run id is trimmed rather than the cell, for the same reason the
	// container name trims it: the cell is what tells two live networks apart.
	if room := limit - len(tail) - 1; len(prefix) > room {
		if room < 1 {
			return tail[max(0, len(tail)-limit):]
		}
		prefix = prefix[:room]
	}
	return prefix + "-" + tail
}

func containerName(runID, cell, host string) string {
	tail := host
	if cell != "" {
		tail = cell + "-" + host
	}
	tail = unsafeName.ReplaceAllString(tail, "-")

	const limit = 60
	prefix := unsafeName.ReplaceAllString("testease-"+runID, "-")
	if room := limit - len(tail) - 1; len(prefix) > room {
		if room < 1 {
			return tail[max(0, len(tail)-limit):]
		}
		prefix = prefix[:room]
	}
	return prefix + "-" + tail
}

// --- the target -----------------------------------------------------------

type container struct {
	provider *Provider
	name     string
	spec     target.Spec
	session  *backend.Session
	labels   map[string]string
	identity target.Identity
	printer  *fingerprint.Printer

	// network is the cell's own network. A container beside this one has to
	// join it to reach this one at all.
	network string
}

// Network is the network this container is on, so a drive step's container can
// be attached to the same one.
func (c *container) Network() (string, bool) { return c.network, c.network != "" }

var (
	_ target.Target      = (*container)(nil)
	_ target.Sessioner   = (*container)(nil)
	_ target.MediaStager = (*container)(nil)
)

func (c *container) Identity() target.Identity { return c.identity }
func (c *container) Labels() map[string]string { return c.labels }
func (c *container) Name() string              { return c.name }

func (c *container) Session(context.Context) (target.Session, error) { return c.session, nil }

// Stage checks that the media a scenario declared actually arrived. The
// strategy is the backend's and it was chosen once: docker mounts at create
// time, so by the time anything could stage, the files are either there or the
// container was built wrong.
// Stage checks that what the host declared actually arrived. It does not put
// it there: a linux target gets its media as mounts at run time and a Windows
// one is copied into between create and start, because a mount resolves on the
// daemon's host and copy is refused once a Windows container is running. Either
// way the question here is the same - is it there - and asking it is what turns
// a silent absence into a refusal.
func (c *container) Stage(ctx context.Context, items []target.Media) error {
	// This does not put anything there - the backend did that when it brought
	// the container up, by whichever means that platform allows. What is left
	// is the question worth asking: did it arrive. A silent absence would be
	// measured as an empty directory.
	for _, m := range items {
		there, err := c.session.Exists(ctx, m.Dest)
		if err != nil {
			return fmt.Errorf("check media %s on %s: %w", m.Dest, c.name, err)
		}
		if !there {
			return fmt.Errorf("media %s is not present on %s: it did not arrive from %s",
				m.Dest, c.name, m.Source)
		}
		if m.SHA256 != "" {
			got, err := c.session.SHA256(ctx, m.Dest)
			if err != nil {
				return fmt.Errorf("media %s on %s is not the readable file staged from %s: %w (the source path must exist on the Docker daemon; for a remote Docker daemon, mirror it there or use a shared path)",
					m.Dest, c.name, m.Source, err)
			}
			if !strings.EqualFold(got, m.SHA256) {
				return fmt.Errorf("media %s on %s has %s, expected %s from %s",
					m.Dest, c.name, got, m.SHA256, m.Source)
			}
		}
	}
	return nil
}

// fingerprinted is a container with an instrument configured. It is a
// separate type because the capability is a real fact about the target.
type fingerprinted struct {
	*container
	*fingerprint.Printer
}

var _ target.Fingerprinter = (*fingerprinted)(nil)

func unwrap(t target.Target) (*container, bool) {
	switch v := t.(type) {
	case *container:
		return v, true
	case *fingerprinted:
		return v.container, true
	case *endpointed:
		return v.container, true
	case *endpointedAndFingerprinting:
		return v.container, true
	default:
		return nil, false
	}
}

// endpointed is a container that answers on a port. It is a separate type for
// the same reason the fingerprinting one is: having a network endpoint is a
// real fact about a target, and a container that answers on nothing is
// honestly not an Endpointer rather than one that returns an address nobody
// can reach.
type endpointed struct {
	*container
	url url.URL
}

var _ target.Endpointer = (*endpointed)(nil)

// Endpoint is where something outside the target reaches it. A browser-driving
// step uses this and never the execution surface.
func (e *endpointed) Endpoint() (url.URL, bool) { return e.url, true }

type endpointedAndFingerprinting struct {
	*endpointed
	printer target.Fingerprinter
}

func (e *endpointedAndFingerprinting) Snapshot(ctx context.Context) (target.Snapshot, error) {
	return e.printer.Snapshot(ctx)
}

func (e *endpointedAndFingerprinting) Diff(ctx context.Context, before target.Snapshot) (target.Change, error) {
	return e.printer.Diff(ctx, before)
}

// withEndpoint gives a target its endpoint when the host declared a port and
// the container has an address to answer on.
func (p *Provider) withEndpoint(c *container, printer target.Fingerprinter) target.Target {
	address, _ := c.identity.Resolved["address"].(string)
	if c.spec.Port == 0 || address == "" {
		if printer != nil {
			return &fingerprinted{container: c, Printer: c.printer}
		}
		return c
	}

	// Declared, never inferred from the port: TLS is served on 8443 as readily
	// as on 443, so a number-based guess is wrong on the boxes most often
	// driven. Empty stays http, which is what a host that declares no scheme
	// already gets.
	scheme := c.spec.Scheme
	if scheme == "" {
		scheme = "http"
	}
	e := &endpointed{
		container: c,
		url:       url.URL{Scheme: scheme, Host: net.JoinHostPort(address, strconv.Itoa(c.spec.Port))},
	}
	if printer != nil {
		return &endpointedAndFingerprinting{endpointed: e, printer: printer}
	}
	return e
}
