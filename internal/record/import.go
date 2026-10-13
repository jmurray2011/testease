package record

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// ImportOptions says what the rows being read belong to.
type ImportOptions struct {
	// RunID is the id the run was published under. It is given rather than
	// minted: the corpus already cites it, and a run that changed its id on
	// being imported would break every finding that named it.
	RunID string

	Scenario string

	// Host is what to record for rows that name none.
	Host string
}

// Import turns a seven-column run.tsv into a record.
//
// This exists so that evidence taken before testease did can still be read by a
// judge. What comes out is a record with a shorter provenance and it says so:
// driver adhoc, because the structure was vouched for by a shell rather than by
// a manifest runner. A reader and a judge may treat it with a longer spoon.
//
// Nothing is repaired on the way through. A -1 means the value would not parse
// when it was written, and a `?` means the instrument said something nobody
// recognised - both were deliberate, and importing them as legitimate readings
// would quietly mend defects the old record made a point of keeping. Rows that
// are not seven fields are kept with a warning, because that corpus was written
// before the guard that made seven fields certain, and a step that happened
// still has to leave a trace.
func Import(dir string, tsv io.Reader, opt ImportOptions) error {
	w, err := Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	runID := opt.RunID
	if runID == "" {
		runID = filepath.Base(dir)
	}
	if _, err := w.Emit(KindRunStarted, 0, RunStarted{
		RunID:    runID,
		Scenario: ScenarioRef{Name: opt.Scenario},
		Driver:   DriverAdhoc,
		Host:     hostOfImport(),
	}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(tsv)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	// Split on newlines only. bufio's own line splitter drops a trailing
	// carriage return, and a CR in one of these claims is genuinely what the
	// tool printed - translating it rewrote the claim in most of the published
	// corpus once already, which is a corpus edit dressed up as tidying.
	scanner.Split(splitOnNewline)
	seq := 0
	line := 0

	for scanner.Scan() {
		line++
		text := scanner.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		fields := strings.Split(text, "\t")
		if line == 1 && len(fields) > 0 && fields[0] == "seq" {
			continue
		}

		seq++
		if len(fields) != 7 {
			if _, err := w.Emit(KindStepStarted, seq, StepStarted{
				Label: text, Host: opt.Host,
			}); err != nil {
				return err
			}
			if err := w.Warn(0, seq, Warning{
				About:    fmt.Sprintf("line %d", line),
				Message:  fmt.Sprintf("%d fields, not seven; kept as written", len(fields)),
				Recorded: "the line verbatim, as the step's label",
			}); err != nil {
				return err
			}
			continue
		}

		host, label := fields[1], fields[2]
		if host == "" {
			host = opt.Host
		}
		if _, err := w.Emit(KindStepStarted, seq, StepStarted{Label: label, Host: host}); err != nil {
			return err
		}

		rc, rcOK := strconv.Atoi(fields[3])
		secs, secsOK := strconv.ParseFloat(fields[4], 64)
		if rcOK != nil {
			rc = -1
		}
		if secsOK != nil {
			secs = -1
		}

		opSeq, err := w.Emit(KindOperation, seq, Operation{
			RC:        rc,
			DurationS: secs,
			Claim:     NewClaim(fields[6], ExtractorLastLine),
			Status:    StatusMeasured,
		})
		if err != nil {
			return err
		}
		if rcOK != nil || secsOK != nil {
			if err := w.Warn(opSeq, seq, Warning{
				About:    "an imported row",
				Message:  fmt.Sprintf("exit %q, seconds %q: not numbers", fields[3], fields[4]),
				Recorded: "-1",
			}); err != nil {
				return err
			}
		}

		fp, known := driftToFingerprint(fields[5])
		fpSeq, err := w.Emit(KindFingerprint, seq, fp)
		if err != nil {
			return err
		}
		if !known {
			if err := w.Warn(fpSeq, seq, Warning{
				About:    "an imported row",
				Message:  fmt.Sprintf("drift %q was already not a reading when it was written", fields[5]),
				Recorded: string(FingerprintUnknown),
			}); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read the rows: %w", err)
	}

	_, err = w.Emit(KindRunFinished, 0, RunFinished{Status: StatusMeasured})
	return err
}

// splitOnNewline is bufio.ScanLines without the part that eats carriage
// returns.
func splitOnNewline(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// driftToFingerprint reads the old column back. The mapping is the render's,
// run backwards, and `?` stays unknown because by the time it was written the
// instrument's own word had already been lost.
func driftToFingerprint(drift string) (Fingerprint, bool) {
	switch drift {
	case "yes":
		return Fingerprint{Result: FingerprintChanged}, true
	case "no":
		return Fingerprint{Result: FingerprintUnchanged}, true
	case "na":
		return Fingerprint{Result: FingerprintUnavailable}, true
	case "err":
		return Fingerprint{Result: FingerprintErrored}, true
	default:
		return Fingerprint{Result: FingerprintUnknown, Raw: drift}, false
	}
}

func hostOfImport() HostRef {
	return HostRef{User: "imported", Hostname: "unknown", PID: 0}
}
