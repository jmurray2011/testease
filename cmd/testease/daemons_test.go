package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Two contexts can reach one daemon - Docker Desktop answers as both default
// and desktop-linux - and looking through both would list, and then remove,
// every target twice. Each daemon is looked at once, through the first
// context that reaches it; a context that does not answer is said, not
// looked through.
func TestEachDaemonIsLookedAtOnce(t *testing.T) {
	ids := map[string]string{"default": "DAEMON-A", "desktop-linux": "DAEMON-A", "winhost": "DAEMON-B"}
	kept, notices := distinctDaemons([]string{"default", "desktop-linux", "dockersrv", "winhost"},
		func(name string) (string, error) {
			if id, ok := ids[name]; ok {
				return id, nil
			}
			return "", errors.New("no answer within 30s")
		})
	if want := []string{"default", "winhost"}; !slices.Equal(kept, want) {
		t.Errorf("looked through %q, want %q", kept, want)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "dockersrv") || !strings.Contains(notices[0], "no answer") {
		t.Errorf("notices = %q", notices)
	}
}
