package runner

// Commands a precondition failure can tell a person to run. They are named here
// so a test can check them against the real command tree; a remedy naming a
// command that does not exist fails the build rather than a run.
const (
	RemedyBackends = "testease backends"
	RemedyDoctor   = "testease doctor"
)

// Remedies is every command-naming remedy this package prints.
func Remedies() []string {
	return []string{RemedyBackends, RemedyDoctor}
}
