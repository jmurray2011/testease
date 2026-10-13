// Package runner sequences a scenario against its targets and records what
// happened. It reaches no conclusions: the runner says measured, an operation
// says whatever it said about itself, a fingerprint says changed or not, and
// none of those is ever derived from another. Judging is downstream and
// somewhere else.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jmurray2011/testease/internal/inputbundle"
	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/target"
)

// PreconditionError is a refusal before or instead of measuring, carrying the
// command that fixes it. Every precondition failure names its remedy: a
// refusal a person cannot act on costs a whole run to learn twice.
type PreconditionError struct {
	Message string
	Remedy  string
}

func (e *PreconditionError) Error() string {
	if e.Remedy == "" {
		return e.Message
	}
	return e.Message + "\n  try: " + e.Remedy
}

// Options is everything a run needs that the scenario does not carry.
type Options struct {
	// Plan is a single resolved scenario. A census uses Manifest and Scope
	// instead, because it resolves the scenario once per cell.
	Plan    *scenario.Plan
	RunsDir string
	Tool    record.ToolRef

	// Measurement is what was configured OUTSIDE the scenario and changes
	// what this run measures: the artifact store an axis discovers from, the
	// fingerprint instrument's own commands, which daemon holds the box, what
	// a one-shot Windows cell gets. It goes onto the record beside the
	// machine, because two runs of one scenario that were instrumented
	// differently measured different things, and nothing afterwards could tell
	// them apart.
	//
	// Only values that change a READING belong here. Where the record is
	// written and where scenarios were read from do not, and a credential
	// never does.
	Measurement map[string]string

	// Inputs are verified, content-addressed external bytes. Their logical
	// identities are recorded; their staged paths are used only to bind media.
	Inputs map[string]inputbundle.Resolved

	// Manifest and Scope make this run a census: every cell of the scope is
	// swept, each against its own targets and into its own sub-record.
	Manifest *scenario.Manifest
	Scope    *Scope

	// Params are values that apply to every cell of a census, over and above
	// the axis values each cell carries. A single-target run has them already
	// resolved into its Plan.
	Params map[string]string

	// Parallel bounds how many cells are live at once. Zero means one.
	Parallel int

	// Resume is the run id of a census to continue. It reruns every cell of
	// that census that is not measured, and nothing else. Explicit, never
	// automatic: lineage is recorded so that a resumed census can never be
	// mistaken for a single complete one.
	Resume string

	// RunID names this run. A caller supplies it when something outside the
	// runner has to agree with it - a provider labelling the targets it
	// creates, a log line a person will match against the record. Empty means
	// the runner names the run itself.
	RunID string

	// Keep leaves targets standing after a clean run. A run that did not
	// finish cleanly keeps them regardless: a half-mutated box answers
	// questions the record does not, unless ReleaseFailedTargets explicitly
	// trades that forensic target for bounded disposable-lab capacity.
	Keep bool

	ReleaseFailedTargets bool

	Providers map[string]target.Provider

	// Driver runs a container on this machine, for steps that drive a target
	// through its endpoint rather than running on it. Nil means this testease
	// cannot run such a step, and says so rather than skipping it.
	Driver Driver

	// DriveSecrets are environment values a drive step's container needs and
	// the record must not keep - an API key a suite authenticates with. They
	// are injected into every drive step and written nowhere: a manifest is
	// committed and params are recorded, so neither can hold one.
	DriveSecrets map[string]string

	// Observe receives lifecycle events after the record writer accepted them.
	// It is presentation only: observers cannot alter evidence or stop a run,
	// and they are never called for an event the writer rejected.
	Observe func(ProgressEvent)

	// Log carries diagnostics: what testease itself did, for someone working
	// out why. It is a fourth channel, apart from the evidence in the record,
	// the notices above, and a probe's own output - and a record is never
	// reconstructed from it. Nil is fine and says nothing.
	Log *slog.Logger
}

// ProgressEvent is the small amount of context a live presentation needs
// around an event. Data is the same typed payload handed to the record writer;
// consumers select fields from known kinds and must not serialize it blindly.
type ProgressEvent struct {
	RunID string
	Cell  string
	Kind  record.Kind
	Step  int
	Total int
	Data  any
}

// Outcome is what the runner can say about a run: its status axis, never a
// verdict.
type Outcome struct {
	RunID  string
	Dir    string
	Status record.Status

	// Total and Measured count units of work - steps in a single-target run,
	// cells in a census. They are kept apart so that a census can say it
	// closed over cells it never measured, which is the difference between a
	// record that covered a set and one that merely accounted for it.
	Total    int
	Measured int

	// Cells is how a census closed, per status. Nil for a single-target run.
	Cells map[record.Status]int
}

// Run executes a resolved scenario and writes its record.
//
// It returns an Outcome whenever a record exists, error or not: a run that
// failed still measured something, and the caller needs to be able to point at
// the evidence.
func Run(ctx context.Context, opt Options) (*Outcome, error) {
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	if opt.Scope != nil || opt.Resume != "" {
		return runCensus(ctx, opt)
	}
	if opt.Plan == nil {
		return nil, errors.New("no scenario to run")
	}

	runID := opt.RunID
	if runID == "" {
		runID = record.RunID(opt.Plan.Name, time.Now())
	}
	return runOne(ctx, opt, opt.Plan, filepath.Join(opt.RunsDir, runID), runID, "")
}

// runOne measures one scenario against one set of targets, into one record.
// A single-target run is one of these; so is every cell of a census.
func runOne(ctx context.Context, opt Options, plan *scenario.Plan, dir, runID, cell string) (*Outcome, error) {
	started := time.Now()

	w, err := record.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open record: %w", err)
	}
	defer func() { _ = w.Close() }()

	// Both keys on every line, so a CI log can be searched against a record.
	log := opt.Log.With("run", runID)
	if cell != "" {
		log = log.With("cell", cell)
	}
	opt.Log = log

	r := &run{
		opt:            opt,
		plan:           plan,
		cell:           cell,
		runID:          runID,
		w:              w,
		dir:            dir,
		targets:        make(map[string]target.Target),
		sessions:       make(map[string]target.Session),
		outcome:        &Outcome{RunID: runID, Dir: dir, Total: len(plan.Steps)},
		requestedMedia: declaredHostMedia(plan),

		// Measured is the floor, not the zero value: an unset Status ranks
		// with measured, and a run that never worsened would otherwise read as
		// though something had gone wrong with it.
		worst: record.StatusMeasured,
	}
	defer r.cleanupSecretFiles()
	defer r.releaseAll(ctx)

	runErr := r.execute(ctx, runID)
	r.close(started, runErr)
	return r.outcome, runErr
}

type run struct {
	opt  Options
	plan *scenario.Plan

	// cell is the census cell this run is, empty for a single-target run.
	cell string

	// runID names the run, for the labels a drive step's container carries.
	runID string

	w        *record.Writer
	dir      string
	targets  map[string]target.Target
	sessions map[string]target.Session
	outcome  *Outcome

	// worst is the run's status so far on the runner's axis. It only ever
	// gets worse, and nothing about success enters it.
	worst record.Status

	// lastSeq is the sequence number of the event just written, so a repair
	// can point at what it repaired.
	lastSeq int

	// workspace is the directory a drive step's container writes into, alive
	// only for the length of that step.
	workspace string

	// secretDir is run-scoped working material outside the immutable record.
	// It is removed after targets release, whether the run measured or not.
	secretDir string

	// requestedMedia is captured before logical inputs and secret files are
	// bound to runtime paths. Target provenance says what the scenario asked
	// for; daemon-visible cache and secret paths are execution details.
	requestedMedia map[string][]string
}

func (r *run) execute(ctx context.Context, runID string) error {
	r.emit(record.KindRunStarted, 0, record.RunStarted{
		RunID:          runID,
		Scenario:       scenarioRef(r.plan.Name, r.plan.ContentIdentity),
		Judge:          declaredJudge(r.plan),
		Reproduce:      declaredReproduce(r.plan),
		Views:          declaredViews(r.plan),
		SecretFiles:    slices.Sorted(maps.Keys(r.plan.Secrets)),
		Tool:           r.opt.Tool,
		Driver:         record.DriverManifest,
		Params:         r.plan.Params,
		Host:           hostRef(),
		EnvFingerprint: r.envFingerprint(),
	})
	for _, name := range slices.Sorted(maps.Keys(r.opt.Inputs)) {
		r.emit(record.KindInputResolved, 0, recordedInput(r.opt.Inputs[name]))
	}
	if err := inputbundle.Bind(r.plan, r.opt.Inputs); err != nil {
		r.worsen(record.StatusInfraFail)
		return &PreconditionError{Message: err.Error(), Remedy: "check the scenario's @input mounts and --input-bundle entries"}
	}
	if err := r.prepareSecretFiles(); err != nil {
		r.worsen(record.StatusInfraFail)
		return err
	}

	if err := r.resolveTargets(ctx); err != nil {
		r.worsen(record.StatusInfraFail)
		return err
	}
	if err := r.checkCapabilities(); err != nil {
		r.worsen(record.StatusInfraFail)
		return err
	}
	if err := r.stageMedia(ctx); err != nil {
		r.worsen(record.StatusInfraFail)
		return err
	}
	r.noteSessionless()

	for i, step := range r.plan.Steps {
		if ctx.Err() != nil {
			// Stop scheduling. The record still closes, saying so.
			r.worsen(record.StatusInterrupted)
			return nil
		}
		if err := r.step(ctx, i+1, step); err != nil {
			return err
		}
	}
	return nil
}

const runtimeDir = ".testease-runtime"

type runtimeOwner struct {
	RunID string `json:"run_id"`
	Cell  string `json:"cell,omitempty"`
	User  string `json:"user"`
	Host  string `json:"host"`
	PID   int    `json:"pid"`
}

func (r *run) prepareSecretFiles() error {
	if len(r.plan.Secrets) == 0 {
		return nil
	}
	parent := filepath.Join(r.opt.RunsDir, runtimeDir, r.runID)
	dir := parent
	if r.cell != "" {
		dir = filepath.Join(parent, r.cell)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return &PreconditionError{Message: "make room for secret files: " + err.Error()}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return &PreconditionError{
				Message: fmt.Sprintf("secret workspace %s already exists; refusing to reuse bytes from an earlier process", dir),
				Remedy:  "testease reap --force   # after confirming the owning process is gone",
			}
		}
		return &PreconditionError{Message: "make secret workspace: " + err.Error()}
	}
	r.secretDir = dir

	host, _ := os.Hostname()
	owner, err := json.Marshal(runtimeOwner{
		RunID: r.runID, Cell: r.cell, User: os.Getenv("USER"), Host: host, PID: os.Getpid(),
	})
	if err != nil {
		return &PreconditionError{Message: "describe secret workspace owner: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), append(owner, '\n'), 0o600); err != nil {
		return &PreconditionError{Message: "write secret workspace owner: " + err.Error()}
	}

	paths := make(map[string]string, len(r.plan.Secrets))
	for _, name := range slices.Sorted(maps.Keys(r.plan.Secrets)) {
		path := filepath.Join(dir, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return &PreconditionError{Message: fmt.Sprintf("create secret file %q: %v", name, err)}
		}
		if err := file.Close(); err != nil {
			return &PreconditionError{Message: fmt.Sprintf("close secret file %q: %v", name, err)}
		}
		paths[name] = path
	}
	if err := scenario.BindMountSources(r.plan, "%", paths); err != nil {
		return &PreconditionError{Message: err.Error()}
	}
	return nil
}

func (r *run) cleanupSecretFiles() {
	if r.secretDir == "" {
		return
	}
	if err := os.RemoveAll(r.secretDir); err != nil {
		r.opt.Log.Warn("could not remove secret workspace", "path", r.secretDir, "error", err)
		return
	}
	// Remove now-empty run and runtime parents without ever recursing above the
	// exact workspace this runner owned. A sibling cell still in flight simply
	// makes either Remove return ENOTEMPTY, which is fine.
	_ = os.Remove(filepath.Dir(r.secretDir))
	_ = os.Remove(filepath.Join(r.opt.RunsDir, runtimeDir))
	r.secretDir = ""
}

func recordedInput(input inputbundle.Resolved) record.InputResolved {
	return record.InputResolved{
		Name: input.Name, Kind: input.Kind, RequestedPath: input.RequestedPath,
		SHA256: input.SHA256, GitCommit: input.GitCommit, Size: input.Size,
		DescriptorHash: input.DescriptorHash,
	}
}

// --- targets --------------------------------------------------------------

func (r *run) resolveTargets(ctx context.Context) error {
	for _, name := range slices.Sorted(maps.Keys(r.plan.Hosts)) {
		host := r.plan.Hosts[name]
		spec := target.Spec{
			Host:        name,
			Cell:        r.cell,
			Provider:    host.Provider,
			Backend:     host.Backend,
			Address:     host.Address,
			Origin:      host.Origin,
			Image:       host.Image,
			Port:        host.Port,
			Scheme:      host.Scheme,
			Probe:       firstProbe(r.plan, name),
			Media:       r.media(host.Media),
			Env:         host.Env,
			BootTimeout: time.Duration(host.BootTimeout),
			Ready:       host.Ready,
		}
		requested := map[string]any{
			"provider": host.Provider,
			"image":    host.Image,
			"media":    r.requestedMedia[name],
		}
		if host.Backend != "" {
			requested["backend"] = host.Backend
		}
		if host.Address != "" {
			requested["address"] = host.Address
		}
		if host.Origin != "" {
			requested["origin"] = host.Origin
		}

		provider, ok := r.opt.Providers[host.Provider]
		if !ok {
			r.emit(record.KindTargetResolved, 0, record.TargetResolved{
				Host:      name,
				Requested: requested,
				Resolved:  map[string]any{"error": fmt.Sprintf("no provider named %q", host.Provider)},
				Facts:     map[string]string{"error": "the target was never dialed"},
			})
			return &PreconditionError{
				Message: fmt.Sprintf("host %q asks for provider %q, which testease does not have", name, host.Provider),
				Remedy:  RemedyBackends,
			}
		}

		t, err := provider.Resolve(ctx, spec)
		if err != nil {
			// The event is written whatever happened: a layer that could not
			// be resolved records its failure rather than being left out, so
			// that a gap can never read as an omission.
			r.emit(record.KindTargetResolved, 0, record.TargetResolved{
				Host:      name,
				Requested: requested,
				Resolved:  map[string]any{"error": err.Error()},
				Facts:     map[string]string{"error": "the target was never dialed"},
			})
			return fmt.Errorf("resolve host %q: %w", name, err)
		}

		r.targets[name] = t
		identity := t.Identity()
		r.emit(record.KindTargetResolved, 0, record.TargetResolved{
			Host:      name,
			Requested: merge(requested, identity.Requested),
			Resolved:  nonNilAny(identity.Resolved),
			Facts:     nonNilString(identity.Facts),
		})
	}
	return nil
}

func declaredHostMedia(plan *scenario.Plan) map[string][]string {
	out := make(map[string][]string, len(plan.Hosts))
	for name, host := range plan.Hosts {
		out[name] = slices.Clone(host.Media)
	}
	return out
}

// noteSessionless marks a scenario whose steps cannot see each other.
//
// A one-shot target runs each command in its own container, so nothing a step
// does is visible to the next. A scenario written for a session and run against
// one of these measures a pristine machine from its second step on - and the
// record looks clean, because every step really did run and really did report.
// That is the exact shape this tool exists to catch, so it is marked rather
// than refused: some scenarios genuinely are a set of independent readings, and
// the runner has no way to tell which.
func (r *run) noteSessionless() {
	steps := map[string]int{}
	for _, step := range r.plan.Steps {
		steps[step.Host]++
	}

	for _, name := range slices.Sorted(maps.Keys(r.targets)) {
		if steps[name] < 2 {
			continue
		}
		if _, hasSession := r.targets[name].(target.Sessioner); hasSession {
			continue
		}
		if _, oneShot := r.targets[name].(target.OneShotter); !oneShot {
			continue
		}
		r.warn(r.lastSeq, 0, []record.Warning{{
			About: fmt.Sprintf("host %q", name),
			Message: fmt.Sprintf(
				"this host has no session, so its %d steps do not share state: each runs in its own container and starts from the image",
				steps[name],
			),
			Recorded: "the steps ran anyway",
		}})
	}
}

// checkCapabilities refuses before measuring rather than after, and says what
// would fix it.
func (r *run) checkCapabilities() error {
	for _, requirement := range r.plan.HostRequirements() {
		t := r.targets[requirement.Host]
		if slices.ContainsFunc(requirement.AnyOf, func(need scenario.Capability) bool { return has(t, need) }) {
			continue
		}
		where := fmt.Sprintf("scenario %s", r.plan.Name)
		if requirement.Step != "" {
			where = fmt.Sprintf("step %q", requirement.Step)
		}
		return &PreconditionError{
			Message: fmt.Sprintf("%s requires host %q to offer %s", where, requirement.Host, alternatives(requirement.AnyOf)),
			Remedy:  RemedyBackends + "   # which capabilities each backend has",
		}
	}
	return nil
}

func alternatives(needs []scenario.Capability) string {
	names := make([]string, 0, len(needs))
	for _, need := range needs {
		names = append(names, string(need))
	}
	return strings.Join(names, " or ")
}

func has(t target.Target, need scenario.Capability) bool {
	switch need {
	case scenario.NeedsSession:
		_, ok := t.(target.Sessioner)
		return ok
	case scenario.NeedsOneShot:
		_, ok := t.(target.OneShotter)
		return ok
	case scenario.NeedsEndpoint:
		_, ok := t.(target.Endpointer)
		return ok
	case scenario.NeedsFingerprint:
		_, ok := t.(target.Fingerprinter)
		return ok
	case scenario.NeedsMedia:
		_, ok := t.(target.MediaStager)
		return ok
	case scenario.NeedsCarry:
		_, ok := t.(target.CarryReceiver)
		return ok
	default:
		return false
	}
}

func (r *run) stageMedia(ctx context.Context) error {
	for _, name := range slices.Sorted(maps.Keys(r.plan.Hosts)) {
		items := r.media(r.plan.Hosts[name].Media)
		if len(items) == 0 {
			continue
		}
		stager, ok := r.targets[name].(target.MediaStager)
		if !ok {
			if _, oneShot := r.targets[name].(target.OneShotter); oneShot {
				// A one-shot cell has no session and no life between steps, so
				// there is no separate call in which to stage anything: its
				// media travels with each command, into a container that is
				// merely created. The strategy is the backend's, and this one
				// has already chosen.
				continue
			}
			return &PreconditionError{
				Message: fmt.Sprintf("host %q declares media, and its backend has no way to put files on a target", name),
				Remedy:  RemedyBackends + "   # which backends stage media",
			}
		}
		for i, item := range items {
			mediaName, size, sha256 := r.describeMedia(item)
			started := record.MediaStageStarted{
				Host: name, Item: i + 1, Total: len(items), Name: mediaName,
				Dest: item.Dest, Size: size, SHA256: sha256,
			}
			r.emit(record.KindMediaStarted, 0, started)
			began := time.Now()
			if err := stager.Stage(ctx, []target.Media{item}); err != nil {
				return fmt.Errorf("stage media %d of %d on host %q: %w", i+1, len(items), name, err)
			}
			r.emit(record.KindMediaStaged, 0, record.MediaStaged{
				Host: name, Item: i + 1, Total: len(items), Name: mediaName,
				Dest: item.Dest, Size: size, SHA256: sha256,
				DurationS: time.Since(began).Seconds(),
			})
		}
	}
	return nil
}

func (r *run) describeMedia(item target.Media) (name string, size int64, sha256 string) {
	for _, inputName := range slices.Sorted(maps.Keys(r.opt.Inputs)) {
		input := r.opt.Inputs[inputName]
		if item.Source == input.StagedPath {
			return input.Name, input.Size, input.SHA256
		}
	}

	name = path.Base(filepath.ToSlash(item.Dest))
	if name == "." || name == "/" || name == "" {
		name = "declared media"
	}
	if r.secretDir != "" {
		if rel, err := filepath.Rel(r.secretDir, item.Source); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return name, 0, ""
		}
	}
	if info, err := os.Stat(item.Source); err == nil && info.Mode().IsRegular() {
		size = info.Size()
	}
	return name, size, item.SHA256
}

// oneShotter is the target's one-shot surface, when that is what it has. A
// target with a session is never run this way even if it could be: a session
// keeps state between steps, and a scenario written for one would measure a
// different machine each step otherwise.
func (r *run) oneShotter(host string) (target.OneShotter, bool) {
	if _, hasSession := r.targets[host].(target.Sessioner); hasSession {
		return nil, false
	}
	shot, ok := r.targets[host].(target.OneShotter)
	return shot, ok
}

func (r *run) session(ctx context.Context, host string) (target.Session, error) {
	if s, ok := r.sessions[host]; ok {
		return s, nil
	}
	sessioner, ok := r.targets[host].(target.Sessioner)
	if !ok {
		return nil, &PreconditionError{
			Message: fmt.Sprintf("host %q has no execution surface, and this scenario runs probes on it", host),
			Remedy:  RemedyBackends + "   # which backends offer a session",
		}
	}
	s, err := sessioner.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("open session on host %q: %w", host, err)
	}
	r.sessions[host] = s
	return s, nil
}

func (r *run) releaseAll(ctx context.Context) {
	for _, s := range r.sessions {
		_ = s.Close()
	}
	// A run that did not finish cleanly normally leaves its targets standing: a
	// half-mutated box answers questions the record does not. A resource-bound
	// disposable lane can explicitly choose release over that forensic target.
	keep := r.opt.Keep || (r.worst != record.StatusMeasured && !r.opt.ReleaseFailedTargets)

	for name, t := range r.targets {
		provider, ok := r.opt.Providers[r.plan.Hosts[name].Provider]
		if !ok {
			continue
		}
		if err := provider.Release(ctx, t, keep); err != nil {
			r.opt.Log.Warn("could not release host", "host", name, "error", err)
		}
	}
}

// --- one step -------------------------------------------------------------

func (r *run) step(ctx context.Context, seq int, step scenario.Step) error {
	log := r.opt.Log.With("step", seq, "label", step.Label, "host", step.Host)
	log.Debug("step starting", "probe", step.Probe, "timeout", time.Duration(step.Timeout))

	// Before anything can go wrong. Invariant 1 is structural here, not
	// disciplinary: even a kill -9 now leaves this trace.
	var carry *record.CarryRef
	if step.Carry != nil {
		carry = &record.CarryRef{From: step.Carry.From, ToHost: step.Carry.ToHost, To: step.Carry.To}
	}
	r.emit(record.KindStepStarted, seq, record.StepStarted{
		Label:    step.Label,
		Host:     step.Host,
		Probe:    step.Probe,
		Carry:    carry,
		Args:     step.Args,
		TimeoutS: int(time.Duration(step.Timeout).Seconds()),
		Control:  string(step.Control),
	})

	// A drive step runs beside the target rather than on it, so it needs no
	// session and no probe file. Everything after the execution is the same:
	// one step, one operation, one claim, recorded the same way.
	// Three ways a step can run, and the difference is a real one about the
	// target rather than a flavour of the same thing: on a persistent
	// execution surface, as a container that exists only for this command, or
	// beside the target through its endpoint.
	oneShot, isOneShot := r.oneShotter(step.Host)

	var session target.Session
	var script []byte
	if step.Carry != nil {
		var err error
		if session, err = r.session(ctx, step.Host); err != nil {
			return err
		}
	} else if !isDrive(step.Probe) {
		var err error
		if !isOneShot {
			if session, err = r.session(ctx, step.Host); err != nil {
				return err
			}
		}
		script, err = os.ReadFile(filepath.Join(r.plan.Dir, filepath.FromSlash(step.Probe)))
		if err != nil {
			return &PreconditionError{
				Message: fmt.Sprintf("step %q needs probe %s: %v", step.Label, step.Probe, err),
				Remedy:  fmt.Sprintf("ls %s", filepath.Join(r.plan.Dir, filepath.FromSlash(step.Probe))),
			}
		}
	}

	before, printer, snapErr := r.snapshot(ctx, seq, step)

	// Fresh names, because a resumed cell appends a second attempt into the
	// same directory: attempt one's Operation events cite its logs by path,
	// and truncating them would rewrite recorded evidence exactly when the
	// failed attempt's logs get read.
	outPath, errPath := record.StepPaths(seq, step.Label)
	outPath, errPath = r.freshPath(outPath), r.freshPath(errPath)
	stdout, err := r.stepFile(outPath)
	if err != nil {
		return err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := r.stepFile(errPath)
	if err != nil {
		return err
	}
	defer func() { _ = stderr.Close() }()

	// The claim is selected while the stream goes past, so a step that
	// produces gigabytes never has its log read back to find one line.
	outTail := &tail{w: stdout}
	errTail := &tail{w: stderr}
	structured := &structuredChannel{}

	opts := target.RunOpts{
		Env:        env(step.Env),
		Stdout:     outTail,
		Stderr:     errTail,
		Structured: structured,
		Timeout:    time.Duration(step.Timeout),
		Fetch:      r.wanted(step),
	}

	wall := time.Now()
	var result target.Result
	var runErr error
	var driven []record.Fact
	var drivenWarnings []record.Warning
	if step.Carry != nil {
		result, driven, runErr = r.carry(ctx, step, session, outTail)
	} else if isDrive(step.Probe) {
		// The logs have to be on disk before the report can be harvested from
		// them, so the drive flushes its own streams first.
		result, runErr = r.driveStep(ctx, seq, step, opts, stdout, stderr, outPath, errPath, &driven, &drivenWarnings)

		// A step that could not be attempted at all is a precondition failure,
		// not a measurement: there is no container runtime here, or the target
		// has no endpoint to drive. Those are fixed by the person running it,
		// so the run stops and says which.
		var precondition *PreconditionError
		if errors.As(runErr, &precondition) {
			return runErr
		}
	} else if isOneShot {
		// One command, one container, and nothing to exec into afterwards.
		result, runErr = oneShot.RunOnce(ctx, script, step.Args, opts)
	} else {
		result, runErr = session.RunScript(ctx, script, step.Args, opts)
	}
	elapsed := result.Duration
	if elapsed == 0 {
		elapsed = time.Since(wall)
	}

	facts, claim, warnings := structured.parse()
	facts = append(facts, driven...)
	warnings = append(warnings, drivenWarnings...)
	// What the BACKEND had to tolerate - above all evidence it could not take
	// out of the target. Without these a cell that came back short of its
	// readings is indistinguishable from one that measured nothing.
	for _, said := range result.Warnings {
		warnings = append(warnings, record.Warning{
			About:    fmt.Sprintf("step %d (%s)", seq, step.Label),
			Message:  said,
			Recorded: "nothing for what it could not reach",
		})
	}
	for _, f := range facts {
		r.emit(record.KindFact, seq, f)
	}

	status := record.StatusMeasured
	rc := result.RC
	switch {
	case runErr != nil && errors.Is(runErr, context.Canceled):
		// The person at the keyboard stopped the run mid-step. That is not
		// the substrate failing and not the probe failing; a record that read
		// infra-fail here would blame the weather for a ctrl-C.
		status = record.StatusInterrupted
		rc = -1
		log.Warn("step interrupted", "error", runErr)
	case runErr != nil:
		// The probe never reported. That is the substrate's failure, not the
		// probe's, and an exit code we did not receive is not zero.
		status = record.StatusInfraFail
		rc = -1
		log.Warn("step did not report", "error", runErr)
	case result.TimedOut:
		status = record.StatusProbeFail
	}

	if claim.Text == "" && claim.Extractor == "" {
		claim = record.NewClaim(firstNonEmpty(outTail.Last(), errTail.Last()), record.ExtractorLastLine)
	}

	opSeq := r.emit(record.KindOperation, seq, record.Operation{
		RC:        rc,
		DurationS: elapsed.Seconds(),
		Stdout:    outPath,
		Stderr:    errPath,
		Claim:     claim,
		Status:    status,
	})
	r.warn(opSeq, seq, warnings)
	r.worsen(status)
	if status == record.StatusMeasured {
		r.outcome.Measured++
	}

	fingerprint := r.fingerprint(ctx, seq, step, before, printer, snapErr)
	r.checkControl(seq, step, rc, fingerprint, facts)
	r.collect(ctx, session, step, seq, rc, result.Fetched)
	r.dropWorkspace()

	if err := r.w.Sync(); err != nil {
		log.Warn("could not flush the record", "error", err)
	}
	log.Debug("step finished", "rc", rc, "status", status, "duration", elapsed)
	return nil
}

// snapshot takes the before reading, if this step asked to be fingerprinted
// and the target can be. A snapshot that failed is returned as its error: the
// instrument exists and could not read, which is a different statement from
// the capability being absent.
func (r *run) snapshot(ctx context.Context, seq int, step scenario.Step) (target.Snapshot, target.Fingerprinter, error) {
	if !step.Fingerprint {
		return target.Snapshot{}, nil, nil
	}
	printer, ok := r.targets[step.Host].(target.Fingerprinter)
	if !ok {
		return target.Snapshot{}, nil, nil
	}
	before, err := printer.Snapshot(ctx)
	if err != nil {
		r.opt.Log.Warn("could not take a before reading", "step", seq, "label", step.Label, "error", err)
		return target.Snapshot{}, printer, err
	}
	return before, printer, nil
}

func (r *run) fingerprint(ctx context.Context, seq int, step scenario.Step, before target.Snapshot, printer target.Fingerprinter, snapErr error) record.FingerprintResult {
	if !step.Fingerprint {
		return ""
	}
	if printer == nil {
		// Unavailable is a statement about the target: it has no such
		// capability. It is the runner's observation, not an instrument's,
		// because there was no instrument.
		r.emit(record.KindFingerprint, seq, record.Fingerprint{Result: record.FingerprintUnavailable})
		return record.FingerprintUnavailable
	}
	if snapErr != nil {
		// The instrument is there and its before reading failed. Unavailable
		// would claim the capability was absent; errored is the statement
		// about the instrument that this actually is.
		fpSeq := r.emit(record.KindFingerprint, seq, record.Fingerprint{Result: record.FingerprintErrored})
		r.warn(fpSeq, seq, []record.Warning{{
			About:    "fingerprint " + step.Label,
			Message:  snapErr.Error(),
			Recorded: "fingerprint: errored",
		}})
		return record.FingerprintErrored
	}

	change, err := printer.Diff(ctx, before)
	if err != nil {
		fpSeq := r.emit(record.KindFingerprint, seq, record.Fingerprint{
			Result:     record.FingerprintErrored,
			Instrument: before.Instrument,
		})
		r.warn(fpSeq, seq, []record.Warning{{
			About:    "fingerprint " + step.Label,
			Message:  err.Error(),
			Recorded: "fingerprint: errored",
		}})
		r.opt.Log.Warn("fingerprint failed", "step", seq, "label", step.Label, "error", err)
		return record.FingerprintErrored
	}

	var boundary record.Boundary
	event := record.Fingerprint{Instrument: before.Instrument}
	switch change.Result {
	case target.Changed:
		event.Result = record.FingerprintChanged
	case target.Unchanged:
		event.Result = record.FingerprintUnchanged
	case target.Errored:
		event.Result = record.FingerprintErrored
	default:
		// An instrument saying something nobody recognizes is neither passed
		// through nor tidied into a legitimate value.
		event = boundary.Fingerprint("fingerprint.result", string(change.Result))
		event.Instrument = before.Instrument
	}

	if len(change.Detail) > 0 {
		name := r.freshPath(ArtifactsDir + "/" + record.StepSlug(seq, step.Label) + ".fingerprint.diff")
		if err := os.WriteFile(filepath.Join(r.dir, filepath.FromSlash(name)), change.Detail, 0o644); err != nil {
			r.opt.Log.Warn("could not keep the fingerprint diff", "step", seq, "error", err)
		} else {
			event.Detail = name
		}
	}

	fpSeq := r.emit(record.KindFingerprint, seq, event)
	r.warn(fpSeq, seq, boundary.Warnings())
	return event.Result
}

// checkControl records the runner's own check of a declared expectation. It is
// a convenience for fail-fast visibility, not the authority: the expect judge
// re-derives the same conclusion from the events themselves.
func (r *run) checkControl(seq int, step scenario.Step, rc int, fingerprint record.FingerprintResult, facts []record.Fact) {
	if step.Expect == nil {
		return
	}
	expected := map[string]string{}
	observed := map[string]string{}
	ok := true

	if step.Expect.RC != nil {
		expected["rc"] = fmt.Sprint(*step.Expect.RC)
		observed["rc"] = fmt.Sprint(rc)
		ok = ok && rc == *step.Expect.RC
	}
	if step.Expect.Fingerprint != "" {
		expected["fingerprint"] = step.Expect.Fingerprint
		if fingerprint != "" {
			observed["fingerprint"] = string(fingerprint)
		}
		// A reading nobody took stays absent from observed rather than present
		// and empty - blank is a value, and this is the lack of one - and an
		// expectation it cannot meet.
		ok = ok && string(fingerprint) == step.Expect.Fingerprint
	}
	if len(step.Expect.Facts) > 0 {
		// The last reading of a key is the one the probe stands behind, the way
		// the shell's `tail -1` did. Both stay in the events.
		read := make(map[string]record.Fact, len(facts))
		for _, f := range facts {
			read[f.Key] = f
		}
		for key, want := range step.Expect.Facts {
			field := record.FactPrefix + key
			expected[field] = want
			got, taken := read[key]
			if !taken || got.Value == nil {
				// A reading nobody took is left absent rather than written down
				// as an empty string: blank is a value, and this is the lack of
				// one. What that means for the verdict is the judge's - it says
				// unverifiable, not regressed.
				ok = false
				continue
			}
			observed[field] = *got.Value
			ok = ok && *got.Value == want
		}
	}

	r.emit(record.KindControlChecked, seq, record.ControlChecked{
		Label:    step.Label,
		Expected: expected,
		Observed: observed,
		OK:       ok,
	})
}

// wanted is what this step declared it would leave behind, so a transport with
// no life after the command can take it while it still has one.
func (r *run) wanted(step scenario.Step) []string {
	var out []string
	for _, a := range r.plan.Artifacts {
		if a.Step == step.Label {
			out = append(out, a.From)
		}
	}
	return out
}

// collect keeps what a step declared, from wherever that step can be reached.
//
// Three transports, three answers, and none of them is "reach for the session
// and hope": a step on a persistent surface is asked afterwards, a one-shot
// cell handed its files back while it still existed, and a drive step left them
// in the workspace on this machine - fetching those through the target's
// session would read the wrong box entirely.
func (r *run) collect(ctx context.Context, session target.Session, step scenario.Step, seq, rc int, fetched map[string][]byte) {
	for _, a := range r.plan.Artifacts {
		if a.Step != step.Label {
			continue
		}
		if a.When == "fail" && rc == 0 {
			continue
		}

		body, err := r.artifact(ctx, session, step, a, fetched)
		if err != nil {
			r.opt.Log.Warn("could not keep artifact", "artifact", a.As, "step", step.Label, "error", err)
			r.warn(r.lastSeq, seq, []record.Warning{{
				About:    "artifact " + a.As,
				Message:  err.Error(),
				Recorded: "nothing",
			}})
			continue
		}
		if err := r.keep(a, body); err != nil {
			r.opt.Log.Warn("could not keep artifact", "artifact", a.As, "step", step.Label, "error", err)
			// A collision is the record's business, not just the log's: what a
			// finding cites by that name is the earlier attempt's bytes, and
			// only the record can say this attempt's were not kept.
			r.warn(r.lastSeq, seq, []record.Warning{{
				About:    "artifact " + a.As,
				Message:  err.Error(),
				Recorded: "nothing",
			}})
		}
	}
}

func (r *run) artifact(ctx context.Context, session target.Session, step scenario.Step, a scenario.Artifact, fetched map[string][]byte) ([]byte, error) {
	if body, ok := fetched[a.From]; ok {
		return body, nil
	}
	if isDrive(step.Probe) {
		return r.fromWorkspace(a)
	}
	if session == nil {
		return nil, fmt.Errorf("this step ran on a target with nothing to fetch through, and kept nothing named %s", a.From)
	}
	return r.fromSession(ctx, session, a)
}

func (r *run) fromSession(ctx context.Context, session target.Session, a scenario.Artifact) ([]byte, error) {
	body, err := session.Fetch(ctx, a.From)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

func (r *run) keep(a scenario.Artifact, body []byte) error {
	// The declared name is the only one anything can cite - no event names an
	// artifact - so a later attempt neither overwrites what an earlier one
	// kept nor invents a numbered name beside it. It refuses, and says so.
	rel := ArtifactsDir + "/" + filepath.Base(a.As)
	dest := filepath.Join(r.dir, filepath.FromSlash(rel))
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s is already kept by an earlier attempt, and nothing can cite a second copy of it: this attempt's bytes were not kept", rel)
		}
		return fmt.Errorf("write %s: %w", dest, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// ArtifactsDir is where a run keeps what it was asked to.
const ArtifactsDir = "artifacts"

// --- record plumbing ------------------------------------------------------

func (r *run) close(started time.Time, runErr error) {
	// Worsened onto the run's own axis rather than computed on the side: the
	// deferred releaseAll reads r.worst to decide keep, and a mid-step error
	// that never touched it would tear down the half-mutated box the record
	// implies was kept.
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled):
		r.worsen(record.StatusInterrupted)
	default:
		r.worsen(record.StatusInfraFail)
	}
	status := r.worst
	r.outcome.Status = status

	r.emit(record.KindRunFinished, 0, record.RunFinished{
		Status:    status,
		DurationS: time.Since(started).Seconds(),
	})
	r.summarise()
}

// summarise writes run.json, which is a convenience and not the record. A
// failure to write it is worth saying and nothing more: the events are the
// evidence and they are already on disk.
func (r *run) summarise() {
	rec, err := record.Read(r.dir)
	if err != nil {
		r.opt.Log.Warn("could not rebuild the summary", "error", err)
		return
	}
	if err := record.WriteSummary(r.dir, record.Summarize(rec)); err != nil {
		r.opt.Log.Warn("could not write the summary", "error", err)
	}
}

// emit records an event. A failure to record warns and never aborts the run:
// the end state is wanted however the run went.
func (r *run) emit(kind record.Kind, step int, data any) int {
	seq, err := r.w.Emit(kind, step, data)
	if err != nil {
		r.opt.Log.Warn("could not record an event", "kind", kind, "step", step, "error", err)
	} else if r.opt.Observe != nil {
		r.opt.Observe(ProgressEvent{
			RunID: r.runID, Cell: r.cell, Kind: kind, Step: step,
			Total: r.outcome.Total, Data: data,
		})
	}
	r.lastSeq = seq
	return seq
}

func (r *run) warn(aboutSeq, step int, warnings []record.Warning) {
	if len(warnings) == 0 {
		return
	}
	if err := r.w.Warn(aboutSeq, step, warnings...); err != nil {
		r.opt.Log.Warn("could not record a repair", "step", step, "error", err)
	}
}

func (r *run) worsen(s record.Status) {
	r.worst = record.Worse(r.worst, s)
}

func (r *run) stepFile(rel string) (*os.File, error) {
	// Exclusive: nothing under a record is ever rewritten, and a name that is
	// somehow taken is a bug worth an error rather than a truncation.
	f, err := os.OpenFile(filepath.Join(r.dir, filepath.FromSlash(rel)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open step log: %w", err)
	}
	return f, nil
}

// freshPath is rel when nothing holds it, or the first numbered variant that
// is free. An earlier attempt's files are evidence its events cite by path,
// and evidence is never rewritten.
func (r *run) freshPath(rel string) string {
	taken, answered := r.exists(rel)
	if !answered {
		// The filesystem would not say. Searching on is what hung: every
		// candidate fails the same way, so the loop never ends. The name is
		// used as asked, and the write that follows will fail loudly for the
		// same reason the stat did - which is a visible failure rather than a
		// run that stops responding.
		r.opt.Log.Warn("could not tell whether a path was free; using it as asked", "path", rel)
		return rel
	}
	if !taken {
		return rel
	}

	ext := path.Ext(rel)
	base := strings.TrimSuffix(rel, ext)
	for i := 2; i <= maxFreshAttempts; i++ {
		cand := fmt.Sprintf("%s.%d%s", base, i, ext)
		taken, answered := r.exists(cand)
		if !answered {
			r.opt.Log.Warn("could not tell whether a path was free; using it as asked", "path", cand)
			return cand
		}
		if !taken {
			return cand
		}
	}
	// Every name up to the bound is held. That is a record with a great many
	// attempts in it rather than a broken one, and the last candidate is
	// returned so the step still writes somewhere nameable.
	r.opt.Log.Warn("every numbered variant of a path is taken", "path", rel, "tried", maxFreshAttempts)
	return fmt.Sprintf("%s.%d%s", base, maxFreshAttempts, ext)
}

// maxFreshAttempts bounds the search for a free name. A record with this many
// attempts at one step is pathological on its own; searching past it is how
// an unanswerable stat became a run that never returned.
const maxFreshAttempts = 1000

// exists says whether something holds this path, and whether the filesystem
// answered at all.
//
// The two are different, and collapsing them is what hung the runner: any
// error that was not "no such file" read as "taken", so freshPath searched
// for a free name that could never be found.
func (r *run) exists(rel string) (taken, answered bool) {
	_, err := os.Stat(filepath.Join(r.dir, filepath.FromSlash(rel)))
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, fs.ErrNotExist):
		return false, true
	default:
		return false, false
	}
}

// --- small helpers --------------------------------------------------------

// envFingerprint is the machine this ran on and what was measuring with it.
//
// A key nobody set is absent rather than empty: "" would read as an instrument
// configured to nothing, which is a different claim from one nobody named.
func (r *run) envFingerprint() map[string]string {
	return measurementFingerprint(r.opt.Measurement)
}

// measurementFingerprint is the one place the field is built, so a census
// parent and the cells under it describe their conditions the same way.
func measurementFingerprint(measurement map[string]string) map[string]string {
	env := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	for key, value := range measurement {
		if value == "" {
			continue
		}
		env[key] = value
	}
	return env
}

// declaredJudge carries the scenario's judge onto the record. The runner does
// not run it and has no opinion on it: judging is downstream, and this is the
// scenario saying how it meant to be read.
func declaredJudge(plan *scenario.Plan) *record.JudgeRef {
	if plan.Judge == nil {
		return nil
	}
	return &record.JudgeRef{Name: plan.Judge.Name, Version: plan.Judge.Version, Params: plan.Judge.Params}
}

func declaredReproduce(plan *scenario.Plan) *record.ReproduceRef {
	if plan.Reproduce == nil {
		return nil
	}
	return &record.ReproduceRef{Facts: slices.Clone(plan.Reproduce.Facts)}
}

func declaredViews(plan *scenario.Plan) map[string]record.ViewRef {
	if len(plan.Views) == 0 {
		return nil
	}
	out := make(map[string]record.ViewRef, len(plan.Views))
	for name, view := range plan.Views {
		out[name] = record.ViewRef{Kind: view.Kind, Facts: slices.Clone(view.Facts)}
	}
	return out
}

// firstProbe is what this host's steps run, which a one-shot backend needs
// before it creates anything. Every step on one host uses the same kind of
// probe in practice; the kind is what matters here, not the file.
func firstProbe(plan *scenario.Plan, host string) string {
	for _, s := range plan.Steps {
		if s.Host == host {
			return s.Probe
		}
	}
	return ""
}

func hostRef() record.HostRef {
	name, _ := os.Hostname()
	return record.HostRef{User: os.Getenv("USER"), Hostname: name, PID: os.Getpid()}
}

func media(specs []string) []target.Media {
	items := make([]target.Media, 0, len(specs))
	for _, s := range specs {
		items = append(items, ParseMedia(s))
	}
	return items
}

// media carries a file's identity across the last boundary: from the file
// this process hashed to the object Docker actually mounted. Paths alone are
// not identities, especially when the daemon is remote or has a private /tmp
// namespace.
func (r *run) media(specs []string) []target.Media {
	items := media(specs)
	for i := range items {
		for _, input := range r.opt.Inputs {
			if items[i].Source == input.StagedPath {
				items[i].SHA256 = input.SHA256
				break
			}
		}
		if items[i].SHA256 == "" && !r.secretMedia(items[i].Source) {
			if digest, ok := fileDigest(items[i].Source); ok {
				items[i].SHA256 = digest
			}
		}
	}
	return items
}

func (r *run) secretMedia(source string) bool {
	if r.secretDir == "" {
		return false
	}
	rel, err := filepath.Rel(r.secretDir, source)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func fileDigest(source string) (string, bool) {
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(source)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), true
}

// ParseMedia reads a source:dest[:ro] mount into the target contract's shape.
// The spelling is the manifest's, so the parser is scenario's.
func ParseMedia(spec string) target.Media {
	source, dest, readOnly := scenario.ParseMount(spec)
	return target.Media{Source: source, Dest: dest, ReadOnly: readOnly}
}

func env(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, k+"="+m[k])
	}
	return out
}

func merge(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func nonNilAny(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nonNilString(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
