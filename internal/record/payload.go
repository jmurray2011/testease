package record

import (
	"strings"
	"unicode/utf8"
)

// Status is the runner's axis: measurement completeness, not success. It is
// deliberately not named ok - the runner has no opinion on whether what it
// measured was good news, and the whole design exists to stop the word
// "success" leaking across a boundary.
type Status string

// The statuses. A cell is measured only if every non-skipped step measured.
//
// Unfinished is the one nothing assigns: it is what a reader says about a unit
// that started and never reported, where no status was ever recorded because
// the thing that would have recorded one stopped. It lives here rather than in
// each reader because a verdict file naming it is cited, and two readers using
// two words for one fact would put both into the corpus.
const (
	StatusMeasured    Status = "measured"
	StatusInfraFail   Status = "infra-fail"
	StatusProbeFail   Status = "probe-fail"
	StatusSkipped     Status = "skipped"
	StatusInterrupted Status = "interrupted"
	StatusUnfinished  Status = "unfinished"
)

// Worse is the one ordering of the status axis.
//
// There is exactly one, and everything that rolls statuses up or turns them
// into an exit code uses it. Two orderings is how a record comes to say
// infra-fail about a run whose exit code said probe-fail - both following a
// rule, neither agreeing with the other, and a reader left to guess which the
// run actually was.
//
// The order is what a reader most needs to act on. A probe that died says
// something about the thing under test; infrastructure weather says something
// about the day; an interruption says something about the person at the
// keyboard.
func Worse(a, b Status) Status {
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

func rank(s Status) int {
	switch s {
	case StatusProbeFail:
		return 5
	case StatusInfraFail:
		return 4
	case StatusInterrupted:
		return 3
	case StatusUnfinished:
		return 2
	case StatusSkipped:
		return 1
	case StatusMeasured:
		return 0
	default:
		return 0
	}
}

// Who vouched for the record's structure: the manifest runner, or a script
// driving the thin-CLI verbs. A reader and a judge may treat adhoc with a
// longer spoon; it is still evidence.
const (
	DriverManifest = "manifest"
	DriverAdhoc    = "adhoc"
)

// RunStarted opens a record.
type RunStarted struct {
	RunID    string            `json:"run_id"`
	Scenario ScenarioRef       `json:"scenario"`
	Tool     ToolRef           `json:"tool"`
	Driver   string            `json:"driver"`
	Params   map[string]string `json:"params,omitempty"`
	Matrix   *Matrix           `json:"matrix"`

	// Judge is how the scenario said this record was meant to be read. It is
	// recorded so that judging a two-year-old record needs the record and
	// nothing else - the scenario tree it came from may be long gone. Absent
	// when the scenario declared none; nothing is invented here.
	Judge *JudgeRef `json:"judge,omitempty"`

	Reproduce   *ReproduceRef      `json:"reproducibility,omitempty"`
	Views       map[string]ViewRef `json:"views,omitempty"`
	SecretFiles []string           `json:"secret_files,omitempty"`

	Host           HostRef           `json:"host"`
	EnvFingerprint map[string]string `json:"env_fingerprint,omitempty"`
}

type ReproduceRef struct {
	Facts []string `json:"stable_facts"`
}

type ViewRef struct {
	Kind  string   `json:"kind"`
	Facts []string `json:"facts"`
}

// ScenarioRef pins what exactly ran: content hashes, not just a repo state.
type ScenarioRef struct {
	Name        string            `json:"name"`
	Hash        string            `json:"hash,omitempty"`
	HashVersion string            `json:"hash_version,omitempty"`
	Probes      map[string]string `json:"probes,omitempty"`
	TreeHash    string            `json:"tree_hash,omitempty"`
	Files       map[string]string `json:"files,omitempty"`
}

// ToolRef pins the binary that took the measurement.
type ToolRef struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// InputResolved pins external, non-secret bytes the scenario consumed. The
// requested path is execution provenance; Name and SHA256 are the portable
// identity another runner uses when the same bytes live somewhere else.
type InputResolved struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	RequestedPath  string `json:"requested_path"`
	SHA256         string `json:"sha256"`
	GitCommit      string `json:"git_commit,omitempty"`
	Size           int64  `json:"size"`
	DescriptorHash string `json:"descriptor_hash"`
}

// HostRef identifies the machine the runner ran on, not the target.
type HostRef struct {
	User     string `json:"user,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

// Matrix records the scope of a matrix run and how that scope was determined -
// discovery is host-dependent, and two hosts' idea of "everything" differs
// silently unless the set is written down.
type Matrix struct {
	Axes         map[string][]string `json:"axes"`
	Exclude      []map[string]string `json:"exclude,omitempty"`
	Source       string              `json:"source"`
	DiscoveredOn string              `json:"discovered_on,omitempty"`
	ResumeOf     string              `json:"resume_of,omitempty"`
}

// JudgeRef names a judge and the version of it.
type JudgeRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	// Params is what the scenario told the judge about its own question -
	// which facts a comparison is over. Recorded, so a re-judge years later
	// reads the record rather than hunting for the scenario tree.
	Params map[string][]string `json:"params,omitempty"`
}

// TargetResolved carries the three identity layers, all mandatory. A layer
// that could not be resolved records its failure; fields are never omitted to
// hide a gap, because "SSH connected to 10.2.3.4" and "this was measured
// against image digest Z" must not read the same.
type TargetResolved struct {
	Host      string            `json:"host"`
	Requested map[string]any    `json:"requested"`
	Resolved  map[string]any    `json:"resolved"`
	Facts     map[string]string `json:"facts"`
}

// MediaStageStarted is written before one declared file crosses the final
// boundary onto a target. Name is the portable input name when one exists;
// local source paths and secret-file sizes never enter this event.
type MediaStageStarted struct {
	Host   string `json:"host"`
	Item   int    `json:"item"`
	Total  int    `json:"total"`
	Name   string `json:"name"`
	Dest   string `json:"dest"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// MediaStaged says that the target's staging strategy accepted one file. For
// a content-addressed input, the provider has compared SHA256 with the bytes
// readable at Dest before this event is emitted.
type MediaStaged struct {
	Host      string  `json:"host"`
	Item      int     `json:"item"`
	Total     int     `json:"total"`
	Name      string  `json:"name"`
	Dest      string  `json:"dest"`
	Size      int64   `json:"size,omitempty"`
	SHA256    string  `json:"sha256,omitempty"`
	DurationS float64 `json:"duration_s"`
}

// StepStarted is emitted before the probe executes, so that a step which
// happened always leaves a trace - even under kill -9.
type StepStarted struct {
	Label    string    `json:"label"`
	Host     string    `json:"host"`
	Probe    string    `json:"probe,omitempty"`
	Carry    *CarryRef `json:"carry,omitempty"`
	Args     []string  `json:"args,omitempty"`
	TimeoutS int       `json:"timeout_s,omitempty"`
	Control  string    `json:"control,omitempty"`
}

// CarryRef is the complete cross-host transfer a step requested.
type CarryRef struct {
	From   string `json:"from"`
	ToHost string `json:"to_host"`
	To     string `json:"to"`
}

// Operation is a step's outcome: what the tool did and what it said about
// itself. The claim sits beside the fingerprint and is never read for facts.
type Operation struct {
	RC        int     `json:"rc"`
	DurationS float64 `json:"duration_s"`
	Stdout    string  `json:"stdout,omitempty"`
	Stderr    string  `json:"stderr,omitempty"`
	Claim     Claim   `json:"claim"`
	Status    Status  `json:"status"`
}

// Fact is the structured channel. Values are strings; numeric interpretation
// belongs to a judge. A fact whose probe failed records a null value and the
// error - a missing reading is itself evidence.
type Fact struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
	Probe string  `json:"probe,omitempty"`
	Phase string  `json:"phase,omitempty"`
	Error string  `json:"error,omitempty"`
}

// FingerprintResult is what the change-detection instrument reported.
type FingerprintResult string

// The four legitimate results, plus the marker for anything else. Unavailable
// is a statement about the target (it has no fingerprint capability); errored
// is a statement about the instrument.
const (
	FingerprintChanged     FingerprintResult = "changed"
	FingerprintUnchanged   FingerprintResult = "unchanged"
	FingerprintUnavailable FingerprintResult = "unavailable"
	FingerprintErrored     FingerprintResult = "errored"
	FingerprintUnknown     FingerprintResult = "unknown"
)

// Fingerprint records a change-detection reading. Raw is set only when the
// instrument reported something unrecognized, and keeps that text so the
// unknown is not quietly normalized away.
type Fingerprint struct {
	Result     FingerprintResult `json:"result"`
	Raw        string            `json:"raw,omitempty"`
	Detail     string            `json:"detail,omitempty"`
	Instrument string            `json:"instrument,omitempty"`
}

// ControlChecked is the runner's convenience check of a declared control. It
// is not the authority: the expect judge re-derives the same conclusion from
// the underlying events.
type ControlChecked struct {
	Label    string            `json:"label"`
	Expected map[string]string `json:"expected"`
	Observed map[string]string `json:"observed"`
	OK       bool              `json:"ok"`
}

// FactPrefix marks a field of ControlChecked that is about a reading rather
// than about the step itself: `fact.svc.active` is the fact whose key is
// `svc.active`.
//
// It lives here because this is the wire, and the wire is what both sides
// agree on: the runner writes the field, a judge reads it, and both import
// record. The manifest spells the same prefix from its own side, because
// scenario imports nothing internal.
//
// A reading nobody took is absent from Observed rather than present and empty:
// blank is a value, and this is the lack of one.
const FactPrefix = "fact."

// CellStarted opens one cell of a matrix run, in the parent's event file.
type CellStarted struct {
	Key string `json:"key"`
}

// CellFinished closes one cell, in the parent's event file.
type CellFinished struct {
	Key    string `json:"key"`
	Status Status `json:"status"`
}

// RunFinished closes a record. Its absence is the machine-readable definition
// of an interrupted run.
type RunFinished struct {
	Status    Status         `json:"status"`
	Cells     map[Status]int `json:"cells,omitempty"`
	DurationS float64        `json:"duration_s"`
}

// Warning is one thing the writer had to repair on the way in: what it was
// about, what was wrong, and what got recorded instead.
type Warning struct {
	About    string `json:"about"`
	Message  string `json:"message"`
	Recorded string `json:"recorded"`
}

// ReportFact is the fact a step records to say where a report it produced
// landed, relative to the record root. A judge finds a report through the
// evidence rather than by convention about filenames, so both sides of that
// have to agree on one key - and this is it.
const ReportFact = "report"

// BaselineKeyFact lets a scenario name the baseline its run should be compared
// against, rather than have one derived from the scenario and its parameters.
const BaselineKeyFact = "baseline.key"

// ClaimMax caps a stored claim. Beyond it the claim is marked truncated.
const ClaimMax = 512

// ExtractorLastLine is the default claim selector: the last non-empty line the
// tool printed. It is a shell convention rather than a property of operations,
// so a probe that knows its tool's output may declare a better one.
const ExtractorLastLine = "last-line"

// Claim is testimony: what the tool said about itself, recorded to sit beside
// the fingerprint. Extractors select a claim; they never transform one.
type Claim struct {
	Text      string `json:"text"`
	Extractor string `json:"extractor"`
	Truncated bool   `json:"truncated"`
}

// NewClaim flattens and caps a claim for storage. The cap lands on a rune
// boundary: a mid-rune cut would make the encoder substitute U+FFFD, which is
// a transform, and extractors do not transform.
func NewClaim(text, extractor string) Claim {
	flat := Flatten(text)
	if len(flat) <= ClaimMax {
		return Claim{Text: flat, Extractor: extractor}
	}
	cut := ClaimMax
	for cut > 0 && !utf8.RuneStart(flat[cut]) {
		cut--
	}
	return Claim{Text: flat[:cut], Extractor: extractor, Truncated: true}
}

// Flatten replaces tabs and newlines with spaces so a value cannot break the
// one-line, tab-separated shape a rendering depends on.
//
// Carriage returns are deliberately left alone. The Windows arm's claims
// arrive CRLF-terminated and a stray CR is genuinely what the tool printed;
// translating it rewrote the claim in most of the published corpus, which is a
// corpus edit dressed up as a validation fix. It also breaks neither half of
// the contract: lines split on \n and fields on \t, and a CR is ordinary text
// to both.
func Flatten(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}
