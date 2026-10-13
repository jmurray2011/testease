package record_test

import (
	"testing"
	"unicode/utf8"

	"github.com/jmurray2011/testease/internal/record"
)

// The parse boundary is where values that arrived as text become recorded
// values. Everywhere else the runner hands the writer typed data and there is
// nothing to fail. These cases are the Go successors of rec__int and
// rec__drift, and they are the reason invariants 2 and 3 have anywhere to
// live at all.

func TestBoundaryIntNeverInventsZero(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want int
		warn bool
	}{
		{"a real exit code is untouched", "137", 137, false},
		{"zero is a reading, not a repair", "0", 0, false},
		{"negative codes survive", "-1", -1, false},
		{"letters are not a number", "abc", -1, true},
		{"empty is not a number", "", -1, true},
		{"whitespace is not a number", "  ", -1, true},
		{"a float is not an exit code", "3.5", -1, true},
		{"a stray sign is not a number", "-", -1, true},
		{"an embedded sign is not a number", "1-2", -1, true},
		{"trailing junk is not a number", "137x", -1, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var b record.Boundary
			got := b.Int("operation.rc", c.raw)
			if got != c.want {
				t.Errorf("Int(%q) = %d, want %d", c.raw, got, c.want)
			}
			warned := len(b.Warnings()) > 0
			if warned != c.warn {
				t.Errorf("Int(%q) warned = %v, want %v", c.raw, warned, c.warn)
			}
			if !c.warn {
				return
			}
			w := b.Warnings()[0]
			if w.About != "operation.rc" {
				t.Errorf("warning about = %q, want %q", w.About, "operation.rc")
			}
			if w.Recorded != "-1" {
				t.Errorf("warning recorded = %q, want %q", w.Recorded, "-1")
			}
			if w.Message == "" {
				t.Error("warning carries no message")
			}
		})
	}
}

func TestBoundarySecondsNeverInventsZero(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want float64
		warn bool
	}{
		{"41.3", 41.3, false},
		{"3", 3, false},
		{"0", 0, false},
		{"abc", -1, true},
		{"", -1, true},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			t.Parallel()

			var b record.Boundary
			got := b.Seconds("operation.duration_s", c.raw)
			if got != c.want {
				t.Errorf("Seconds(%q) = %v, want %v", c.raw, got, c.want)
			}
			if warned := len(b.Warnings()) > 0; warned != c.warn {
				t.Errorf("Seconds(%q) warned = %v, want %v", c.raw, warned, c.warn)
			}
		})
	}
}

func TestBoundaryFingerprintPassesTheFourThrough(t *testing.T) {
	t.Parallel()

	for _, want := range []record.FingerprintResult{
		record.FingerprintChanged,
		record.FingerprintUnchanged,
		record.FingerprintUnavailable,
		record.FingerprintErrored,
	} {
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()

			var b record.Boundary
			got := b.Fingerprint("fingerprint.result", string(want))
			if got.Result != want {
				t.Errorf("Fingerprint(%q).Result = %q, want %q", want, got.Result, want)
			}
			if got.Raw != "" {
				t.Errorf("a recognized result kept raw = %q, want it empty", got.Raw)
			}
			if len(b.Warnings()) != 0 {
				t.Errorf("a recognized result warned: %+v", b.Warnings())
			}
		})
	}
}

func TestBoundaryFingerprintKeepsTheRawUnknown(t *testing.T) {
	t.Parallel()

	// Neither passed through (a judge would read it as a real reading) nor
	// rewritten to a legitimate one. "unknown" is neither, and the original
	// text survives beside it - dropping it would be normalizing.
	var b record.Boundary
	got := b.Fingerprint("fingerprint.result", "maybe")

	if got.Result != record.FingerprintUnknown {
		t.Errorf("Result = %q, want %q", got.Result, record.FingerprintUnknown)
	}
	if got.Raw != "maybe" {
		t.Errorf("Raw = %q, want %q", got.Raw, "maybe")
	}
	if len(b.Warnings()) != 1 {
		t.Fatalf("warnings = %d, want 1", len(b.Warnings()))
	}
	if rec := b.Warnings()[0].Recorded; rec != string(record.FingerprintUnknown) {
		t.Errorf("warning recorded = %q, want %q", rec, record.FingerprintUnknown)
	}
}

func TestBoundaryAccumulatesWarnings(t *testing.T) {
	t.Parallel()

	var b record.Boundary
	b.Int("operation.rc", "abc")
	b.Seconds("operation.duration_s", "xyz")
	b.Fingerprint("fingerprint.result", "maybe")

	if got := len(b.Warnings()); got != 3 {
		t.Fatalf("warnings = %d, want 3", got)
	}
	abouts := []string{"operation.rc", "operation.duration_s", "fingerprint.result"}
	for i, want := range abouts {
		if got := b.Warnings()[i].About; got != want {
			t.Errorf("warning %d about = %q, want %q", i, got, want)
		}
	}
}

func TestClaimFlattensButPreservesCR(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want string
	}{
		{"plain text survives", "tables=25", "tables=25"},
		{"a tab becomes a space", "tables=25\tenv=beta", "tables=25 env=beta"},
		{"a newline becomes a space", "line one\nline two", "line one line two"},
		{
			// Translating this rewrote the claim in most of the published
			// corpus. A stray CR is genuinely what the tool printed.
			"a carriage return is preserved",
			"done\r",
			"done\r",
		},
		{"CRLF keeps its CR", "done\r\n", "done\r "},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := record.NewClaim(c.text, record.ExtractorLastLine)
			if got.Text != c.want {
				t.Errorf("NewClaim(%q).Text = %q, want %q", c.text, got.Text, c.want)
			}
			if got.Truncated {
				t.Error("short claim marked truncated")
			}
			if got.Extractor != record.ExtractorLastLine {
				t.Errorf("Extractor = %q, want %q", got.Extractor, record.ExtractorLastLine)
			}
		})
	}
}

func TestClaimTruncatesOnARuneBoundary(t *testing.T) {
	t.Parallel()

	// A mid-rune cut makes the JSON encoder substitute U+FFFD, which is a
	// transform. Extractors select; they never transform.
	long := ""
	for len(long) < record.ClaimMax+8 {
		long += "é" // two bytes each, so the cap lands mid-rune if cut by byte
	}

	got := record.NewClaim(long, record.ExtractorLastLine)
	if !got.Truncated {
		t.Error("a claim past the cap is not marked truncated")
	}
	if len(got.Text) > record.ClaimMax {
		t.Errorf("claim is %d bytes, want at most %d", len(got.Text), record.ClaimMax)
	}
	if !utf8.ValidString(got.Text) {
		t.Errorf("claim was cut mid-rune: %q", got.Text)
	}
}
