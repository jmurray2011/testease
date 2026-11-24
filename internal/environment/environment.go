// Package environment is every TESTEASE_* variable, and whether it is safe to
// leave set.
//
// One table, so that the CLI's help, the README and the desktop app cannot
// drift apart on what exists. The split is provenance, not taste: a variable
// that changes WHERE things are can sit in a profile forever, and one that
// changes WHAT IS MEASURED cannot, because the record does not pin it and
// nothing afterwards can tell it was there.
package environment

// Var is one variable: its name, what it sets, and whether it is safe to leave
// set in an environment rather than pass as a flag.
type Var struct {
	Name, Sets string
	Safe       bool
}

// Vars is every variable, the safe ones first.
func Vars() []Var {
	return []Var{
		{"TESTEASE_SCENARIO_ROOT", "where scenarios are", true},
		{"TESTEASE_RUNS_DIR", "where records are written", true},
		{"TESTEASE_BASELINES", "the baseline judge's store", true},
		{"TESTEASE_FINDINGS", "findings used by publish", true},
		{"TESTEASE_RUNS_REPO", "repository used by publish", true},
		{"TESTEASE_STATEFILE_DIR", "where a provisioner wrote its state", true},
		{"TESTEASE_TARGET_CATALOG", "where target names and tags are defined", true},
		{"TESTEASE_CAMPAIGN_ROOT", "where campaign definitions are", true},
		{"TESTEASE_INPUT_BUNDLE", "where named, content-addressed inputs are described", true},
		{"TESTEASE_RUNS_URL", "where published records are readable", true},
		{"TESTEASE_AWS_PROFILE", "AWS profile for Systems Manager hosts", true},
		{"TESTEASE_AWS_REGION", "AWS region for Systems Manager hosts", true},
		{"TESTEASE_SSM_TRANSFER_STORE", "temporary store for large SSM media", true},
		{"TESTEASE_WINRM_USER", "user for hosts dialled over WinRM", true},
		{"TESTEASE_WINRM_PASSWORD_FILE", "password file for WinRM hosts", true},
		{"TESTEASE_SSH_USER", "user for hosts dialled over SSH", true},
		{"TESTEASE_SSH_PASSWORD_FILE", "password file for SSH hosts", true},
		{"TESTEASE_SSH_KEY", "private key for SSH hosts", true},
		{"TESTEASE_SSH_KNOWN_HOSTS", "known_hosts file for SSH hosts", true},
		{"TESTEASE_LOG_LEVEL", "debug, info, warn or error", true},
		{"TESTEASE_LOG_FORMAT", "text or json", true},
		{"TESTEASE_STORE", "the artifact store an axis discovers from", false},
		{"TESTEASE_DOCKER_CONTEXT", "which daemon holds the box", false},
		{"TESTEASE_WIN_DOCKER_CONTEXT", "which daemon holds one-shot Windows cells", false},
		{"TESTEASE_INSTRUMENT_NAME", "what to record the instrument as", false},
		{"TESTEASE_INSTRUMENT_COLLECT", "the fingerprint instrument", false},
		{"TESTEASE_INSTRUMENT_DIFF", "how two of its readings are compared", false},
		{"TESTEASE_WIN_MEMORY", "what a one-shot Windows cell gets", false},
		{"TESTEASE_ENV_FILE", "secrets injected into drive steps", false},
		{"TESTEASE_COMPARE", "baseline comparison command", false},
	}
}

// Setting is a variable with what it is set to now. Set is false for a
// variable that is absent or empty, which the house reads the same way.
type Setting struct {
	Var
	Value string
	Set   bool
}

// Current reads every variable through getenv.
func Current(getenv func(string) string) []Setting {
	vars := Vars()
	out := make([]Setting, len(vars))
	for i, v := range vars {
		value := getenv(v.Name)
		out[i] = Setting{Var: v, Value: value, Set: value != ""}
	}
	return out
}
