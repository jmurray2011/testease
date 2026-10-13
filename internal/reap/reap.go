// Package reap decides which targets testease left behind can safely be
// removed.
//
// The default when a run dies midway is to leave its targets standing, and that
// is right: a half-mutated box answers questions the record does not. What was
// always missing is the other half - nothing came back for them, so they
// accumulated until somebody noticed the disk.
//
// What makes this safe is not a heuristic and not an age window. Every target
// testease creates carries the run that made it and the process that ran it, so
// a target whose process is gone cannot belong to a running run. That is a
// proof rather than a guess, and nothing else here counts as one: "it looks
// old" is exactly the reasoning this replaces.
//
// The rule runs the ownership labels backwards, so it has to be exactly as
// precise as the forward one. Too greedy and it deletes somebody's long-lived
// box; too shy and the leak never stops.
package reap

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

// The labels every target testease creates carries. Spelled here as literals
// because this package may import nothing internal; internal/target spells
// the same set for the providers that write them, and the binary's tests hold
// the two spellings together.
const (
	LabelRunner  = "testease.runner"
	LabelRun     = "testease.run"
	LabelPID     = "testease.pid"
	LabelCreated = "testease.created"
	LabelBackend = "testease.backend"
	LabelHost    = "testease.host"
	LabelCell    = "testease.cell"
)

// Fate is what can be said about a target from its labels alone.
type Fate string

// The four answers. Only the first is a reason to remove anything.
const (
	// Orphaned: the process that made it is provably gone.
	Orphaned Fate = "orphaned"

	// Live: its process is still running, so the run may still be going.
	Live Fate = "live"

	// Foreign: another user's, on this machine. Their business.
	Foreign Fate = "foreign"

	// Unprovable: nothing here can be shown either way - a target testease did
	// not create, one made on another machine, or labels that do not parse.
	Unprovable Fate = "unprovable"
)

// Runner is who is asking.
type Runner struct {
	User string
	Host string

	// AllUsers widens the question to every user on this machine. It does not
	// widen to other machines, because it cannot: a pid means something on the
	// machine that issued it and nothing anywhere else.
	AllUsers bool
}

// Alive reports whether a process exists.
type Alive func(pid int) bool

// Running is the real test.
//
// Signal 0 asks "could I signal this" without signalling. A pid held by another
// user answers EPERM, which means the process is very much alive - and that
// answer must be read as alive rather than as an error, or the reaper removes
// targets belonging to running work.
func Running(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Decide says what can be proved about one target, and why.
//
// The reason is not decoration. Anything this refuses to remove, somebody will
// eventually have to remove by hand, and they deserve to know what stopped it
// rather than being left to guess whether the tool is being careful or broken.
func Decide(labels map[string]string, self Runner, alive Alive) (Fate, string) {
	runner := labels[LabelRunner]
	rawPID := labels[LabelPID]

	if runner == "" || rawPID == "" {
		return Unprovable, "not labelled by testease, or labelled by a version that did not say " +
			"which process made it; nothing here can be proved either way"
	}

	user, host, hasHost := strings.Cut(runner, "@")
	if !hasHost || host == "" {
		return Unprovable, fmt.Sprintf(
			"labelled %q, which does not say which machine it was made on", runner,
		)
	}

	// A pid is only meaningful on the machine that issued it. Reading one from
	// anywhere else is reading a stranger's process table by coincidence of
	// number.
	if host != self.Host {
		return Unprovable, fmt.Sprintf(
			"made on %s, and a process id from there means nothing here; ask on that machine", host,
		)
	}

	if user != self.User && !self.AllUsers {
		return Foreign, fmt.Sprintf(
			"belongs to %s, not to you; --all-users widens the question to every user on this machine", user,
		)
	}

	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid <= 0 {
		return Unprovable, fmt.Sprintf("labelled with %q, which is not a process id", rawPID)
	}

	if alive(pid) {
		return Live, fmt.Sprintf("process %d is still running, so the run that made it may be too", pid)
	}
	return Orphaned, fmt.Sprintf("process %d is gone, so no running run owns it", pid)
}

// Target is one thing testease left somewhere.
type Target struct {
	// Name is what the daemon calls it.
	Name string

	// Where it lives, for a person who has to go and look: a docker context,
	// an account, whatever the backend calls the place it dialled.
	Where string

	Labels map[string]string

	Fate   Fate
	Reason string
}

// RunID is the run that made this target, for a person deciding whether they
// still care about it.
func (t Target) RunID() string { return t.Labels[LabelRun] }

// Created is when, as the label says. It is printed and never reasoned from.
func (t Target) Created() string { return t.Labels[LabelCreated] }

// Assess decides every target's fate.
func Assess(targets []Target, self Runner, alive Alive) []Target {
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		t.Fate, t.Reason = Decide(t.Labels, self, alive)
		out = append(out, t)
	}
	return out
}

// Removable is the subset a reaper may act on, which is only ever the orphans.
func Removable(targets []Target) []Target {
	var out []Target
	for _, t := range targets {
		if t.Fate == Orphaned {
			out = append(out, t)
		}
	}
	return out
}
