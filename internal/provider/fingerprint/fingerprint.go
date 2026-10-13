// Package fingerprint adapts an external collect/diff instrument to a target.
//
// The collector runs on the target's session. The diff runs beside testease,
// over the two opaque readings. Keeping that adapter independent of any one
// provider lets a container and an SSH machine answer the same evidence
// question with the same instrument.
package fingerprint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jmurray2011/testease/internal/target"
)

// Instrument is an external fingerprinting tool. Collect runs on the target
// and prints one opaque reading. Diff runs locally and exits 0 for unchanged,
// 1 for changed, and anything else for an instrument error.
type Instrument struct {
	Name    string
	Collect []string
	Diff    []string
}

// Printer gives one target fingerprint capability. A Printer belongs to one
// target: readings from parallel matrix cells must never share cleanup.
type Printer struct {
	session    target.Session
	instrument Instrument
	subject    string
	prints     string
}

var _ target.Fingerprinter = (*Printer)(nil)

// New returns a fingerprint adapter for one target session.
func New(session target.Session, instrument Instrument, subject string) *Printer {
	return &Printer{session: session, instrument: instrument, subject: subject}
}

// Close removes working readings. Their conclusion and diff are already in
// the record; these files are not separately owned evidence.
func (p *Printer) Close() {
	if p == nil || p.prints == "" {
		return
	}
	_ = os.RemoveAll(p.prints)
	p.prints = ""
}

// Snapshot takes one opaque reading on the target.
func (p *Printer) Snapshot(ctx context.Context) (target.Snapshot, error) {
	path, err := p.collect(ctx, "before")
	if err != nil {
		return target.Snapshot{}, err
	}
	return target.Snapshot{Instrument: p.instrument.Name, Ref: path}, nil
}

// Diff takes the after reading and asks the instrument to compare it.
func (p *Printer) Diff(ctx context.Context, before target.Snapshot) (target.Change, error) {
	after, err := p.collect(ctx, "after")
	if err != nil {
		return target.Change{}, err
	}

	args := append(append([]string{}, p.instrument.Diff[1:]...), before.Ref, after)
	cmd := exec.CommandContext(ctx, p.instrument.Diff[0], args...)
	out, runErr := cmd.CombinedOutput()
	switch code := exitCode(runErr); code {
	case 0:
		return target.Change{Result: target.Unchanged}, nil
	case 1:
		return target.Change{Result: target.Changed, Detail: out}, nil
	default:
		return target.Change{Result: target.Errored, Detail: out}, nil
	}
}

func (p *Printer) collect(ctx context.Context, phase string) (string, error) {
	dir, err := p.scratch()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s-%d.reading", p.subject, phase, time.Now().UnixNano()))

	out, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("open fingerprint reading: %w", err)
	}
	defer func() { _ = out.Close() }()

	result, err := p.session.Run(ctx, p.instrument.Collect,
		target.RunOpts{Stdout: out, Stderr: io.Discard})
	if err != nil {
		return "", fmt.Errorf("collect a reading from %s: %w", p.subject, err)
	}
	if result.RC != 0 {
		return "", fmt.Errorf("instrument %s exited %d collecting from %s", p.instrument.Name, result.RC, p.subject)
	}
	return path, nil
}

func (p *Printer) scratch() (string, error) {
	if p.prints != "" {
		return p.prints, nil
	}
	dir, err := os.MkdirTemp("", "testease-prints-")
	if err != nil {
		return "", fmt.Errorf("make room for fingerprint readings: %w", err)
	}
	p.prints = dir
	return dir, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
