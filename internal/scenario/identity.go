package scenario

// ContentIdentity pins both the inputs that affect execution and the complete
// scenario tree that supplied them.
type ContentIdentity struct {
	Hash        string
	HashVersion string
	Probes      map[string]string
	TreeHash    string
	Files       map[string]string
}
