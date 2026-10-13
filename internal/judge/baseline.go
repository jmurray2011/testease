package judge

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// The verdicts baseline reaches. Its pass set is promote and established:
// both mean this run is at least as good as what came before it.
//
// There is deliberately no force-promote here. Forcing is a person deciding to
// accept a regression, which is an act on the corpus rather than a reading of
// the evidence - and a judge whose answer changed because someone passed a
// flag would not be a function of the record at all. The judge says hold; who
// overrides a hold, and why, is the caller's business and is said out loud
// where it happens.
const (
	Promote     = "promote"
	Established = "established"
	Hold        = "hold"
)

// The facts this judge reads. They live in the record package because the step
// that writes them and the judge that reads them must mean the same key.
const (
	ReportFact      = record.ReportFact
	BaselineKeyFact = record.BaselineKeyFact
)

// Store is where baselines live. Storage is the store's business; whether to
// advance one is the judge's.
type Store interface {
	// Get opens the baseline under a key. The second result is false when
	// there is none, which is not an error: a first run has nothing to be
	// compared against and establishes the baseline instead.
	Get(key string) (io.ReadCloser, bool, error)

	// Promote replaces the baseline under a key.
	Promote(key string, report io.Reader) error
}

// DirStore keeps baselines as files in a directory, which is what the shell
// this replaces did and is enough for a corpus a person can read.
type DirStore struct{ Dir string }

var _ Store = DirStore{}

// unsafeKey keeps a key usable as a filename. Keys name products and versions,
// so they are short and mostly already safe.
var unsafeKey = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

// Path is where a key's baseline lives.
func (s DirStore) Path(key string) string {
	return filepath.Join(s.Dir, unsafeKey.ReplaceAllString(key, "-")+".json")
}

// Get opens a baseline.
func (s DirStore) Get(key string) (io.ReadCloser, bool, error) {
	f, err := os.Open(s.Path(key))
	switch {
	case err == nil:
		return f, true, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("read baseline %s: %w", key, err)
	}
}

// Promote replaces a baseline. It is called because a verdict said to, never
// as a side effect of reaching one.
func (s DirStore) Promote(key string, report io.Reader) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("make room for baselines: %w", err)
	}
	f, err := os.Create(s.Path(key))
	if err != nil {
		return fmt.Errorf("write baseline %s: %w", key, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, report); err != nil {
		return fmt.Errorf("write baseline %s: %w", key, err)
	}
	return nil
}

// Baseline compares a run's report against the last one accepted for the same
// product and version, and says whether this run should replace it.
//
// It decides; it does not promote. A judge that advanced the baseline as it
// judged would stop being a function of the record: re-judging the same run
// would compare the report against itself and reach a different answer, and a
// verdict nobody can re-derive is not evidence of anything. So the store is
// advanced afterwards, by whoever asked for the judging, when the verdict says
// to - and the verdict pins the baseline it read, so a reader years later can
// tell which comparison this was.
type Baseline struct {
	Store Store

	// Compare is the comparator, configured rather than built in. It is run as
	// `<compare...> <baseline> <report>` and its exit code is the answer: zero
	// for no regressions, anything else for regressions. What counts as a
	// regression is the comparator's to know, and testease never opens either
	// file.
	Compare []string
}

var _ Judge = Baseline{}

// Name is what a finding cites.
func (Baseline) Name() string { return "baseline" }

// Version changes whenever the conclusion this judge would reach changes.
func (Baseline) Version() string { return "1" }

// Judge compares this run's report against the stored baseline.
func (b Baseline) Judge(rec *record.Record) (Result, error) {
	if b.Store == nil || len(b.Compare) == 0 {
		return Result{}, &PreconditionError{
			Message: "the baseline judge needs somewhere to keep baselines and something to compare with",
			Remedy:  "testease judge <run-id> --judge baseline --baselines <dir> --compare '<command>'",
		}
	}

	latest := rec.Latest()
	report, ok := factOf(latest, ReportFact)
	if !ok {
		return Result{}, &NothingToJudgeError{Judge: b.Name()}
	}
	reportPath := filepath.Join(rec.Dir, filepath.FromSlash(report))
	if _, err := os.Stat(reportPath); err != nil {
		return Result{}, fmt.Errorf("the record names a report at %s that is not there: %w", report, err)
	}

	key := b.key(latest)
	baseline, found, err := b.Store.Get(key)
	if err != nil {
		return Result{}, err
	}
	if !found {
		// Nothing to compare against. This run establishes the baseline, and
		// saying "pass" here would claim a comparison nobody made.
		return Result{
			Verdict: Established,
			Steps: map[string]StepVerdict{
				key: {Verdict: Established, Because: []string{"no prior baseline: this run would establish it"}},
			},
		}, nil
	}
	defer func() { _ = baseline.Close() }()

	stored, err := os.CreateTemp("", "testease-baseline-")
	if err != nil {
		return Result{}, fmt.Errorf("stage the baseline: %w", err)
	}
	defer func() {
		_ = stored.Close()
		_ = os.Remove(stored.Name())
	}()
	if _, err := io.Copy(stored, baseline); err != nil {
		return Result{}, fmt.Errorf("stage the baseline: %w", err)
	}
	if err := stored.Close(); err != nil {
		return Result{}, fmt.Errorf("stage the baseline: %w", err)
	}

	args := append(append([]string{}, b.Compare[1:]...), stored.Name(), reportPath)
	out, runErr := exec.Command(b.Compare[0], args...).CombinedOutput() //nolint:gosec // the comparator is configured by the caller, deliberately
	code := exitCode(runErr)
	if code < 0 {
		// The comparator never ran, or was killed before it could exit. There
		// is no comparison here to reach a verdict about, and Hold would be a
		// verdict against the product for a reading nobody took - one Save
		// would then persist against the record.
		return Result{}, fmt.Errorf("the comparator %q could not run: %w", strings.Join(b.Compare, " "), runErr)
	}

	verdict := Hold
	if code == 0 {
		verdict = Promote
	}

	because := []string{fmt.Sprintf("compared against baseline %q", key)}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		because = append(because, strings.Split(trimmed, "\n")...)
	}

	return Result{
		Verdict:       verdict,
		Steps:         map[string]StepVerdict{key: {Verdict: verdict, Because: because}},
		ExtraEvidence: map[string]string{"baseline": hashFile(stored.Name())},
	}, nil
}

// Key is the baseline this record should be compared against.
func (b Baseline) Key(rec *record.Record) string { return b.key(rec.Latest()) }

// key names the baseline: whatever the scenario said, or the scenario and the
// parameters it ran with, which is the same statement made less explicitly.
func (b Baseline) key(rec *record.Record) string {
	if key, ok := factOf(rec, BaselineKeyFact); ok {
		return key
	}
	for _, e := range rec.Events {
		if e.Kind != record.KindRunStarted {
			continue
		}
		started, err := record.Decode[record.RunStarted](e)
		if err != nil {
			continue
		}
		parts := []string{started.Scenario.Name}
		names := make([]string, 0, len(started.Params))
		for name := range started.Params {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			parts = append(parts, name+"-"+started.Params[name])
		}
		return strings.Join(parts, "-")
	}
	return "unnamed"
}

// factOf is the last value a run recorded for a key. Later readings win: a
// scenario that reads a fact before and after an operation means the second.
func factOf(rec *record.Record, key string) (string, bool) {
	value := ""
	found := false
	for _, e := range rec.Events {
		if e.Kind != record.KindFact {
			continue
		}
		fact, err := record.Decode[record.Fact](e)
		if err != nil || fact.Key != key || fact.Value == nil {
			continue
		}
		value, found = *fact.Value, true
	}
	return value, found
}

func hashFile(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return HashBytes(body)
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

// PreconditionError is a judge refusing for want of configuration, with the
// command that supplies it.
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
