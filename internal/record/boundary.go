package record

import (
	"fmt"
	"math"
	"strconv"
)

// Boundary is the parse boundary: where a value that arrived as text - from
// the thin-CLI record verbs, or from probe output on the structured channel -
// becomes a recorded value. Everywhere else the runner hands the writer typed
// data and there is nothing to fail, which is why this is the only place
// invariants 2 and 3 can be broken and therefore the only place they are
// defended.
//
// It never invents a legitimate value for garbage. A number that will not
// parse becomes -1, never 0: printf %d on a non-number printed 0, and 0 in the
// exit column reads as SUCCESS, the worst possible thing for it to invent. An
// instrument reading nobody recognizes becomes "unknown" carrying its raw
// text, which is neither passing it through nor tidying it away.
//
// Each repair is remembered as a Warning for the writer to record beside the
// event it belongs to. The zero value is ready to use, and one Boundary is
// meant to cover one event's worth of fields.
type Boundary struct {
	warnings []Warning
}

// Int converts a textual integer field. Anything that is not an integer
// becomes -1 and leaves a warning.
func (b *Boundary) Int(about, raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		b.warn(about, fmt.Sprintf("not a number: %q", raw), "-1")
		return -1
	}
	return n
}

// Seconds converts a textual duration in seconds. Anything that is not a
// finite number becomes -1 and leaves a warning.
func (b *Boundary) Seconds(about, raw string) float64 {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		b.warn(about, fmt.Sprintf("not a number: %q", raw), "-1")
		return -1
	}
	return v
}

// Fingerprint converts a textual change-detection reading. One of the four
// legitimate results passes through untouched; anything else is recorded as
// unknown with the original text kept beside it.
func (b *Boundary) Fingerprint(about, raw string) Fingerprint {
	switch r := FingerprintResult(raw); r {
	case FingerprintChanged, FingerprintUnchanged, FingerprintUnavailable, FingerprintErrored:
		return Fingerprint{Result: r}
	default:
		b.warn(about, fmt.Sprintf("unknown fingerprint result: %q", raw), string(FingerprintUnknown))
		return Fingerprint{Result: FingerprintUnknown, Raw: raw}
	}
}

// Warnings returns the repairs made so far, in the order they happened.
func (b *Boundary) Warnings() []Warning {
	return b.warnings
}

func (b *Boundary) warn(about, message, recorded string) {
	b.warnings = append(b.warnings, Warning{About: about, Message: message, Recorded: recorded})
}
