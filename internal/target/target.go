// Package target is the contract between the runner and whatever machine it
// is observing. It is declaration only: no I/O, no daemon, no dependency on
// how a record is written.
//
// Capabilities here are plain interfaces checked by type assertion. There is
// no capability registry, no name strings, no dynamic dispatch: four
// capabilities is a list, not a system, and a new one is a code change.
package target

import (
	"context"
	"io"
	"io/fs"
	"net/url"
	"time"
)

// Identity is what a record must be able to say about the machine an
// observation was made against, in three layers that are kept separate on
// purpose.
//
// Requested is what the scenario asked for. Resolved is the artifact that
// actually answered - an image digest, an AMI id, an instance id, never just
// an address. Facts are read off the box after dial. A reader must be able to
// tell "SSH happened to connect to 10.2.3.4" from "this was measured against
// image digest Z", so a layer that could not be resolved records its failure
// rather than being omitted.
type Identity struct {
	Requested map[string]any
	Resolved  map[string]any
	Facts     map[string]string
}

// Target is a machine under observation.
type Target interface {
	Identity() Identity

	// Labels carry ownership on every target testease created: which runner,
	// which run, which pid, when, which backend. Forensic cleanup answers
	// "can I safely remove this" from the label alone, never from "it looks
	// old", so reap trusts these and nothing else.
	Labels() map[string]string
}

// The ownership labels every target testease creates carries. They live here
// because they are the contract between the providers that write them and the
// cleanup that reasons from them, and two spellings of one key would make a
// target unprovable.
const (
	LabelRunner  = "testease.runner"
	LabelRun     = "testease.run"
	LabelPID     = "testease.pid"
	LabelCreated = "testease.created"
	LabelBackend = "testease.backend"
	LabelHost    = "testease.host"
	LabelCell    = "testease.cell"
)

// Spec is a request for a machine, in the terms a provider understands.
// Whatever a scenario's manifest says becomes one of these before a provider
// sees it, so that providers never learn the manifest's shape.
type Spec struct {
	// Host is the logical name the scenario's steps refer to.
	Host string

	// Cell is the census cell this target belongs to, empty for a
	// single-target run. Two cells must never share a target: one that did
	// would measure the other cell's machine and say nothing about it.
	Cell string

	Provider string

	// Backend is how a machine that already exists is dialled - the provider
	// locates it, the backend talks to it, and they are separate questions.
	Backend string

	// Address is where that machine is: a hostname for something dialled over
	// the network, an instance id for something reached through a service.
	Address string

	// Origin is the complete network origin of a target that has no execution
	// surface. It is kept apart from Address: the latter locates a machine for
	// a backend to dial, while an origin is already the public surface a drive
	// step observes.
	Origin string

	Image string
	Media []Media

	// Env is what a created target boots with.
	Env map[string]string

	// BootTimeout raises the backend's own budget for this host. Zero leaves
	// the backend's, which is the measured floor.
	BootTimeout time.Duration

	// Port is what the product answers on, when it answers on one. Zero means
	// this host has no endpoint and cannot be driven from outside.
	Port int

	// Scheme is how the port is spoken to. Empty means http, which is what a
	// host that never declared one gets.
	Scheme string

	// Probe is the probe a one-shot target is about to run. A backend with no
	// session has to know what it is running before it creates anything,
	// because the container IS the command - there is nothing to exec into
	// afterwards.
	Probe string

	// Ready is how this host says it has finished starting: a command that
	// succeeds once it has. Empty leaves it to the backend, whose weakest
	// honest claim is that the execution surface answers at all. What counts
	// as ready is the scenario's knowledge; how long to wait is the
	// backend's, and a caller may raise that budget but never lower it.
	Ready []string
}

// Provider is how a target comes to exist. testease consumes targets; it does
// not provision them, and this interface is the permanent boundary - a
// provisioning platform belongs on the far side of it.
type Provider interface {
	// Resolve may create a target (docker) or merely locate one (static,
	// statefile).
	Resolve(ctx context.Context, spec Spec) (Target, error)

	// Release honours keep. A provider that did not create may do nothing.
	Release(ctx context.Context, t Target, keep bool) error
}

// Sessioner is a target with a persistent execution surface: docker exec,
// SSH, SSM, WinRM.
type Sessioner interface {
	Session(ctx context.Context) (Session, error)
}

// OneShotter is a target that has no persistent session, only
// create-run-capture - Hyper-V containers.
type OneShotter interface {
	RunOnce(ctx context.Context, script []byte, args []string, opt RunOpts) (Result, error)
}

// CarryReceiver is a target that can retain bytes for its next execution
// without opening a persistent session. One-shot targets use it to accept
// carried media before their container is created and started.
type CarryReceiver interface {
	ReceiveCarry(ctx context.Context, body io.Reader, remote string) (CarryReceipt, error)
}

// CarryReceipt records the ordering guarantee a receiver made. A copy into a
// running target is not the same observation as media seeded before start.
type CarryReceipt struct {
	SeededBeforeStart bool
}

// Endpointer is a target with a network endpoint, which is a different
// capability from an execution surface and deliberately so: a browser-driving
// step uses the endpoint and never the execution surface, and a probe uses the
// surface and never the endpoint.
type Endpointer interface {
	Endpoint() (url.URL, bool)
}

// Fingerprinter is a target that supports before/after change detection.
// Where a target does not implement it, drift is recorded unavailable - a
// statement about the target, not a formatting convention.
type Fingerprinter interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	Diff(ctx context.Context, before Snapshot) (Change, error)
}

// Snapshot is one reading taken by a fingerprint instrument, opaque to
// everything but the instrument that took it.
type Snapshot struct {
	// Instrument names and versions what took the reading, and is recorded
	// beside the result: a diff is only meaningful against a reading taken by
	// the same instrument under the same profile.
	Instrument string

	// Ref is however the instrument refers to the reading it stored.
	Ref string
}

// Change is what a fingerprint comparison concluded, plus the evidence for it.
type Change struct {
	// Result is changed, unchanged, or errored. A Fingerprinter never reports
	// unavailable: that is the absence of this capability, which is the
	// runner's observation to make, not the instrument's.
	Result ChangeResult

	// Detail is the instrument's own diff output, retained as an artifact and
	// opaque to testease. Nothing reads it until a judge does.
	Detail []byte
}

// ChangeResult is a fingerprint comparison's conclusion.
type ChangeResult string

// The three a Fingerprinter can reach. Anything else the instrument says is
// the runner's problem to record as unknown, never to translate into one of
// these.
const (
	Changed   ChangeResult = "changed"
	Unchanged ChangeResult = "unchanged"
	Errored   ChangeResult = "errored"
)

// MediaStager is a target that files can reach: by volume mount, by session
// copy, by exec-cat retrieval, or by baking into an image where copy is
// refused. The strategy is the backend's, chosen once, not rediscovered per
// script.
type MediaStager interface {
	Stage(ctx context.Context, items []Media) error
}

// Media is one thing a scenario needs present on its target.
type Media struct {
	Source   string
	Dest     string
	ReadOnly bool

	// SHA256 is the expected identity of a content-addressed file input. It is
	// empty for ordinary mounts and directories. A provider that can read the
	// staged file must compare this with what the target can actually read: a
	// daemon with a private mount namespace can otherwise turn a verified file
	// into an empty directory while still making an existence check pass.
	SHA256 string
}

// Session is a persistent execution surface.
type Session interface {
	// Run executes a command. It does NOT connect stdin: a step whose command
	// happens to read stdin would start consuming the runner's own input,
	// which in a fan-out loop is the loop's input.
	Run(ctx context.Context, cmd []string, opt RunOpts) (Result, error)

	// RunScript ships a script, runs it with args, and returns one ordered
	// log. This is THE primitive - one round trip per step rather than
	// fifteen, so a step's log reads in order. Control flow belongs inside
	// the script.
	RunScript(ctx context.Context, script []byte, args []string, opt RunOpts) (Result, error)

	// Fetch retrieves a file. It must work on paths a copy cannot reach:
	// tmpfs is unreadable by docker cp, measured, so the docker backend
	// implements this by exec-cat instead.
	Fetch(ctx context.Context, remote string) (io.ReadCloser, error)

	// Put places a file on the target.
	Put(ctx context.Context, local io.Reader, remote string, mode fs.FileMode) error

	Close() error
}

// RunOpts is how a caller says where the output goes and how long it may take.
//
// The streams are the caller's writers rather than paths the backend picks:
// where output lands in a record is the runner's business, and a backend that
// knew record paths would be deciding a record's shape from the far side of
// the contract. Output is teed as it arrives, never buffered whole, so a crash
// mid-step keeps everything received so far.
type RunOpts struct {
	Env    []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer

	// Structured is the probe's fd 3: the channel probes write facts to, kept
	// apart from stdout so that a fact is never recovered by parsing
	// testimony. A backend that cannot offer a third descriptor leaves it
	// unwritten rather than folding it into stdout.
	Structured io.Writer

	// Fetch names files to take off the target while it still exists.
	//
	// A backend with a persistent session ignores this and is asked afterwards.
	// A backend without one cannot be: a one-shot cell stops existing when its
	// command ends, so what to keep has to travel with what to run or it
	// cannot be kept at all.
	Fetch []string

	// Timeout bounds this one execution. Zero means the backend's own budget
	// applies. A step may raise a backend's budget and may never silently
	// lower it: a caller pinning a tighter bound than the substrate needs
	// turns "still starting" into a false failure.
	Timeout time.Duration
}

// Result is what an execution reported. Where the output went is the caller's
// own answer - it supplied the writers.
type Result struct {
	// RC is the exit code as the backend reported it, verbatim. -1 appears
	// here only when a backend genuinely could not learn the code.
	RC       int
	Duration time.Duration

	// TimedOut says the execution was cut short by its budget rather than
	// exiting on its own. An exit code from a killed process is not a
	// reading, and a judge must be able to tell the difference.
	TimedOut bool

	// Fetched is what RunOpts.Fetch asked for, by the path it was asked for.
	// A file that was not there is simply absent: a missing artifact is worth
	// a warning, not a failed step.
	Fetched map[string][]byte

	// Warnings are what the backend had to tolerate to produce this result -
	// above all, evidence it could not take out of the target. They are plain
	// sentences because this package depends on nothing; the runner puts them
	// into the record as the warnings they are.
	//
	// A backend that came back short and said nothing here is one whose
	// missing reading cannot be told from a reading nobody took.
	Warnings []string
}
