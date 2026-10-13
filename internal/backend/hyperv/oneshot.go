// Package hyperv runs a probe as a container that exists for one command.
//
// A container that runs one command and exits has no session to open and
// nothing to exec into afterwards. That is a real difference in kind rather
// than a flavour of the same thing, which is why it is a separate capability:
// create, stage, start, capture, remove.
//
// It is named for the Windows containers that motivated it and it is not
// Windows-only. The same shape works on a Linux daemon and is tested on both.
package hyperv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	docker "github.com/jmurray2011/testease/internal/backend/docker"
	"github.com/jmurray2011/testease/internal/target"
)

// Budget is how long a one-shot cell may take.
//
// Generous on purpose: a Windows container's whole job happens in this one
// command, so the budget covers an install rather than a step of one.
const Budget = 45 * time.Minute

// Where a cell finds what it needs.
//
// The container ROOT, and that is measured rather than tidy. A file staged into
// C:\Windows\Temp lands in the layer - docker diff shows it, and it copies
// straight back out - and then start discards it, because per-container writes
// under the system directory do not survive into the running container. Staging
// to the root survives, in both directions, on both daemons. The shell this
// replaces knew it too, and staged to /scenario.ps1 and /fixture.dump without
// ever writing down why.
const (
	probeAt = "/testease-probe"
	factsAt = "/testease-facts"
)

// Windows and Linux differ in one thing here, and it is the shell rather than
// the daemon: PowerShell has no fd 3 to give a probe, so it is given the path
// instead. Systems Manager makes the same distinction for the same reason.
const (
	Windows = "windows"
	Linux   = "linux"
)

// OneShot creates a container, stages what the probe needs into it while it is
// merely created, runs it, and takes back whatever it left.
type OneShot struct {
	Client docker.Client

	// OS is what the daemon runs, and it decides how a probe reaches the cell:
	// the two differ in what they will carry, and the difference is measured
	// rather than assumed.
	OS string

	// Name is the container this cell runs as. It carries the run and the cell
	// so that a leaked one can be tied back to what made it.
	Name string

	// Image is what the cell runs.
	Image string

	// Labels go on the container, so reap can answer from them alone.
	Labels map[string]string

	// Memory is the container's memory bound, in docker's own notation.
	Memory string

	// Media is what the scenario's host declared, staged into the cell in the
	// same window the probe is. A one-shot cell has no session, so there is no
	// separate call in which to put a file on it - whatever a probe needs has
	// to arrive while the container is merely created, because copy is refused
	// once it is running.
	Media []Stage
}

// createArgs builds the create. The image goes last before the command, or
// docker reads the command's own flags as its own.
func (o OneShot) createArgs(command, env []string) []string {
	args := []string{"create", "--name", o.Name}
	if o.Memory != "" {
		args = append(args, "-m", o.Memory)
	}
	for _, k := range slices.Sorted(maps.Keys(o.Labels)) {
		args = append(args, "--label", k+"="+o.Labels[k])
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, o.Image)
	return append(args, command...)
}

// Stage is a file to put inside before the container starts.
type Stage struct {
	Local  string
	Remote string
}

// Run creates the container, runs the probe in it, and removes it.
//
// How the probe gets in depends on the daemon, because the two differ in what
// they will carry: staged as a file where a staged file survives the start, and
// carried inside the command where it does not. Where staging works it happens
// between create and start, because docker cp is refused against a running
// container - which is why this is create-copy-start rather than run.
func (o OneShot) Run(ctx context.Context, script []byte, args []string, opt target.RunOpts) (target.Result, error) {
	if o.Image == "" {
		return target.Result{RC: -1}, fmt.Errorf("a one-shot cell needs an image")
	}

	windows := strings.EqualFold(o.OS, Windows)
	command, stage, err := o.plan(script, args, windows, &opt)
	if err != nil {
		return target.Result{RC: -1}, err
	}

	// A name left over from an earlier cell would make create fail, and a
	// caller that ignored that would attach to the OLD container and report on
	// the previous run's machine.
	_, _ = o.Client.Output(ctx, "rm", "-f", o.Name)

	if _, err := o.Client.Output(ctx, o.createArgs(command, opt.Env)...); err != nil {
		return target.Result{RC: -1}, fmt.Errorf("create one-shot cell %s: %w", o.Name, err)
	}
	defer func() {
		clean, done := cleanupContext(ctx)
		defer done()
		_, _ = o.Client.Output(clean, "rm", "-f", o.Name)
	}()

	for _, st := range stage {
		if _, err := o.Client.Output(ctx, "cp", st.Local, o.Name+":"+st.Remote); err != nil {
			return target.Result{RC: -1}, fmt.Errorf("stage %s into %s: %w", st.Local, o.Name, err)
		}
	}

	budget := opt.Timeout
	if budget == 0 {
		budget = Budget
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	started := time.Now()
	result := target.Result{}
	err = o.Client.Stream(runCtx, opt.Stdout, opt.Stderr, "start", "-a", o.Name)
	result.Duration = time.Since(started)

	// overran is set when the backend's own floor cut the cell short. The
	// readings are still taken - the container is stopped and holds them - and
	// the overrun is reported afterwards, because it is not the probe's.
	var overran error

	switch {
	case err == nil:
		result.RC = 0
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		// Cut short rather than finished. Killing the client did not kill the
		// container, and copy is refused against a running one - so it is
		// stopped here, or the readings it already took are lost with it.
		o.stop(ctx)
		result.RC = -1
		// TimedOut only for a bound the STEP declared. A step that declared
		// none is bounded by Budget, this backend's own floor, and the runner
		// maps TimedOut to probe-fail - so 45 minutes nobody in the scenario
		// chose would become a verdict about the product. docker, ssh and
		// winrm guard this the same way; said as the substrate not coming
		// back, because an rc of -1 with no error reads as a measurement.
		if opt.Timeout > 0 {
			result.TimedOut = true
		} else {
			overran = fmt.Errorf("one-shot cell %s did not finish inside this backend's own %s budget, which its step did not declare: %w",
				o.Name, budget, runCtx.Err())
		}
	case runCtx.Err() != nil:
		// Cancelled from above: the person at the keyboard, not the probe.
		o.stop(ctx)
		result.RC = -1
		return result, runCtx.Err()
	default:
		// WHOSE CODE IS IT. `docker start -a` exits with the CONTAINER's
		// status when the container ran and with its own when it never
		// started, and the number cannot separate the two: measured on a live
		// daemon, a probe whose shell cannot find a tool comes back 127 with
		// the container `exited 127`, while a container docker could not start
		// at all comes back 1 with the container `created 127`. 125/126/127
		// are `docker run`'s create-time conventions and do not apply here.
		//
		// So the container is asked whether it ran. Treating 127 as docker's
		// discarded the no-curl-on-the-appliance reading this lab has already
		// met once - and the early return took that cell's fd3 facts and
		// declared artifacts with it.
		if _, reported := docker.CodeOf(err); !reported {
			result.RC = -1
			return result, fmt.Errorf("start one-shot cell %s: %w", o.Name, err)
		}
		rc, ran := o.containerExit(ctx)
		if !ran {
			result.RC = -1
			return result, fmt.Errorf("start one-shot cell %s: %w", o.Name, err)
		}
		result.RC = rc
	}

	// The container is stopped and still exists, which is the only moment its
	// readings can be taken.
	o.takeFacts(ctx, opt.Structured, &result)
	o.fetchDeclared(ctx, opt.Fetch, &result)
	return result, overran
}

// fetchDeclared copies out what the step declared it would leave behind, while
// the stopped container still exists. A one-shot cell has no life after its
// command, so what to keep travels with what to run - RunOpts.Fetch - or it
// cannot be kept at all. A file that is not there is simply absent: a missing
// artifact is worth a warning upstream, never a failed step.
func (o OneShot) fetchDeclared(ctx context.Context, wanted []string, result *target.Result) {
	for _, remote := range wanted {
		local, err := os.CreateTemp("", "testease-fetch-")
		if err != nil {
			continue
		}
		name := local.Name()
		_ = local.Close()

		clean, done := cleanupContext(ctx)
		_, cpErr := o.Client.Output(clean, "cp", o.Name+":"+cpPath(remote), name)
		expired := clean.Err() != nil
		done()
		if cpErr != nil {
			_ = os.Remove(name)
			if expired {
				// A declared artifact that timed out is not one the cell never
				// produced, and a record cannot tell them apart unless it is
				// told which happened.
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"cell %s declared %s and it could not be taken out within %s",
					o.Name, remote, CleanupBudget,
				))
			}
			continue
		}
		body, err := os.ReadFile(name)
		_ = os.Remove(name)
		if err != nil {
			continue
		}
		if result.Fetched == nil {
			result.Fetched = map[string][]byte{}
		}
		result.Fetched[remote] = body
	}
}

// cpPath spells a remote path the way docker cp will take it: the
// leading-slash form, never a drive letter - the same two-spellings trap
// windowsPath works around in the other direction.
func cpPath(remote string) string {
	p := strings.ReplaceAll(remote, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		p = p[2:]
	}
	return p
}

// plan works out what to run and what to put inside first.
//
// One shape for both daemons: the probe goes in as a file and the readings come
// back as a file. What differs is the shell - PowerShell cannot be handed a
// third descriptor, so it is handed the path - and that is a difference about
// the shell, not about the machine.
func (o OneShot) plan(script []byte, args []string, windows bool, opt *target.RunOpts) ([]string, []Stage, error) {
	remote := probeAt
	if windows {
		// PowerShell will only run -File on something named .ps1.
		remote += ".ps1"
	}

	local, err := os.CreateTemp("", "testease-probe-")
	if err != nil {
		return nil, nil, fmt.Errorf("stage the probe: %w", err)
	}
	defer func() { _ = local.Close() }()

	if _, err := local.Write(script); err != nil {
		return nil, nil, fmt.Errorf("stage the probe: %w", err)
	}
	if err := local.Chmod(0o755); err != nil {
		return nil, nil, fmt.Errorf("stage the probe: %w", err)
	}

	// Named for every probe, opened as fd 3 for the one shell that has them, so
	// a probe written for any other backend runs here unchanged.
	opt.Env = append(append([]string{}, opt.Env...), "TESTEASE_FACTS="+factsAt)

	var command []string
	if windows {
		command = append([]string{
			"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", windowsPath(remote),
		}, args...)
	} else {
		wrapper := fmt.Sprintf("exec 3>%s; exec %s \"$@\"", factsAt, remote)
		command = append([]string{"/bin/sh", "-c", wrapper, "testease"}, args...)
	}

	// The probe first. Measured: staging media ahead of it on a Windows daemon
	// leaves the probe missing at start - the copies are not independent, and
	// the one the container is about to run is the one that must land last.
	stage := []Stage{{Local: local.Name(), Remote: remote}}
	return command, append(stage, o.Media...), nil
}

// windowsPath spells a staged path the way the thing inside will read it.
//
// One location, two spellings, and they are not interchangeable: docker cp
// insists on the leading-slash form and rejects a drive letter, while PowerShell
// wants C:\. Measured - a cell that staged correctly and then could not run
// what it had staged.
func windowsPath(remote string) string {
	return "C:" + strings.ReplaceAll(remote, "/", `\`)
}

// takeFacts copies the readings back while the stopped container still exists.
// stop halts the cell so its readings can be copied out. Bounded and detached:
// the caller may have been cancelled, and that must not leave the container
// running, nor let a daemon that stopped answering hold the interrupt path.
func (o OneShot) stop(ctx context.Context) {
	clean, done := cleanupContext(ctx)
	defer done()
	_, _ = o.Client.Output(clean, "stop", "-t", "10", o.Name)
}

// containerExit is what the container did, asked of the daemon rather than
// inferred from the client's exit code. A container that ran reports its own
// status whatever the number; one still in `created` never ran a process of
// the cell's, so the code beside it belongs to nothing.
func (o OneShot) containerExit(ctx context.Context) (int, bool) {
	clean, done := cleanupContext(ctx)
	defer done()
	out, err := o.Client.Output(clean, "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}}", o.Name)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[0] != "exited" {
		return 0, false
	}
	rc, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return rc, true
}

// takeFacts copies the cell's readings out of the stopped container.
//
// THIS IS EVIDENCE, NOT CLEANUP. It is detached from the caller's context so
// that a ctrl-C does not throw away what the cell already measured, and
// bounded so that a daemon which stopped answering cannot hold the interrupt
// path - but a bound that expires here LOSES readings, so it says so. A cell
// that recorded nothing and a cell whose recordings could not be reached are
// different facts about the run.
func (o OneShot) takeFacts(ctx context.Context, into io.Writer, result *target.Result) {
	if into == nil {
		return
	}
	local, err := os.CreateTemp("", "testease-facts-")
	if err != nil {
		return
	}
	name := local.Name()
	_ = local.Close()
	defer func() { _ = os.Remove(name) }()

	clean, done := cleanupContext(ctx)
	defer done()
	if _, err := o.Client.Output(clean, "cp", o.Name+":"+factsAt, name); err != nil {
		if clean.Err() != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"cell %s finished and its readings could not be taken out of it within %s: %v",
				o.Name, CleanupBudget, clean.Err(),
			))
			return
		}
		// A cell that recorded nothing recorded nothing.
		return
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return
	}
	_, _ = into.Write(body)
}
