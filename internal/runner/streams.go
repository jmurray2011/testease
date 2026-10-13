package runner

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/jmurray2011/testease/internal/record"
)

// tail passes a stream through to its destination and remembers the last
// non-empty line it saw.
//
// The claim is selected as the output goes past rather than by reading the log
// back afterwards: a step that printed gigabytes still costs one line of
// memory, and a step whose process died still has whatever arrived. Nothing
// here alters a byte on its way to the file - the log is the verbatim record,
// carriage returns and all.
type tail struct {
	w    io.Writer
	last string

	// partial holds the bytes since the last newline, because a write
	// boundary is not a line boundary.
	partial []byte
}

func (t *tail) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.scan(p[:n])
	}
	return n, err
}

func (t *tail) scan(p []byte) {
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			t.partial = append(t.partial, p...)
			return
		}
		line := append(t.partial, p[:i]...)
		t.partial = nil
		if s := strings.TrimSpace(string(line)); s != "" {
			// Trim the trailing newline only. A carriage return is genuinely
			// what the tool printed, and translating it rewrites the claim.
			t.last = strings.TrimRight(string(line), "\n")
		}
		p = p[i+1:]
	}
}

// Last is the last non-empty line seen, including anything not yet terminated
// by a newline - a probe killed mid-line still said something.
func (t *tail) Last() string {
	if s := strings.TrimSpace(string(t.partial)); s != "" {
		return string(t.partial)
	}
	return t.last
}

// structuredChannel is the probe's fd 3: the channel a probe writes readings
// to, kept apart from stdout so that a fact is never recovered by parsing
// testimony. Probes never format records; they write lines here and the runner
// turns them into events.
//
// The wire format is one record per line, tab separated:
//
//	fact<TAB>key<TAB>value
//	claim<TAB>text
//
// It is small and in memory on purpose: this channel carries readings, not
// output, and a probe that writes a gigabyte of facts has a different problem.
type structuredChannel struct {
	buf bytes.Buffer
}

func (c *structuredChannel) Write(p []byte) (int, error) { return c.buf.Write(p) }

// parse turns the channel's lines into events. A line it cannot read becomes a
// warning rather than a dropped reading: a probe that tried to say something
// and was not understood is evidence about the probe.
func (c *structuredChannel) parse() (facts []record.Fact, claim record.Claim, warnings []record.Warning) {
	for i, line := range strings.Split(c.buf.String(), "\n") {
		// The terminator is not part of the reading. A PowerShell probe writes
		// CRLF because that is what a line ending is on Windows, and a fact of
		// "yes\r" would never equal the "yes" a scenario declared. Only the
		// terminator goes: everything else on the line is the probe's.
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		where := fmt.Sprintf("structured channel line %d", i+1)
		parts := strings.Split(line, "\t")

		switch parts[0] {
		case "fact":
			if len(parts) < 3 {
				warnings = append(warnings, record.Warning{
					About:    where,
					Message:  fmt.Sprintf("a fact needs a key and a value: %q", line),
					Recorded: "nothing",
				})
				continue
			}
			value := strings.Join(parts[2:], "\t")
			facts = append(facts, record.Fact{Key: parts[1], Value: &value})

		case "no-reading":
			// A reading the probe could not take, recorded as the absence it
			// is. Without this the only way to report "I did not look" is to
			// invent a value for it, and a judge cannot tell an invented value
			// from a measured one - so a step that examined part of a machine
			// would pass on the strength of the part it examined.
			if len(parts) < 3 {
				warnings = append(warnings, record.Warning{
					About:    where,
					Message:  fmt.Sprintf("an unread fact needs a key and a reason: %q", line),
					Recorded: "nothing",
				})
				continue
			}
			facts = append(facts, record.Fact{Key: parts[1], Error: strings.Join(parts[2:], "\t")})

		case "claim":
			if len(parts) < 2 {
				warnings = append(warnings, record.Warning{
					About:    where,
					Message:  fmt.Sprintf("a claim needs text: %q", line),
					Recorded: "nothing",
				})
				continue
			}
			// The probe selected this line; it did not compose it. Extractors
			// select, never transform.
			claim = record.NewClaim(strings.Join(parts[1:], "\t"), "probe")

		default:
			warnings = append(warnings, record.Warning{
				About:    where,
				Message:  fmt.Sprintf("not a fact or a claim: %q", line),
				Recorded: "nothing",
			})
		}
	}
	return facts, claim, warnings
}
