// Package runnerid names who is running testease, the same way on every
// platform.
//
// The name is an ownership label. reap and targets read it back to decide whose
// targets are whose, so a runner that names itself differently from one call
// site to the next would disown its own work.
package runnerid

import "os"

// User is the account running testease: USER where it is set, as on unix, and
// otherwise USERNAME, which is where Windows keeps it.
func User() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("USERNAME")
}

// Runner is the owner label: user@host.
func Runner() string {
	host, _ := os.Hostname()
	return User() + "@" + host
}
