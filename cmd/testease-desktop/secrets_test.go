package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/jmurray2011/testease/internal/record"
)

// Scenarios sometimes pass a credential as a param. The record keeps it - a
// record is never rewritten - but this app does not put it on screen, where
// anyone looking over a shoulder or at a screenshot reads it. The value is
// held back by its name; the name stays, so the person can see there was one.

func TestASecretLookingParamIsNotShown(t *testing.T) {
	test.NewTempApp(t)

	runs := t.TempDir()
	dir := recorded(t, runs, "sso-20261121T083545Z", func(w *record.Writer) {
		emit(t, w, record.KindRunStarted, 0, record.RunStarted{
			RunID: "sso-20261121T083545Z", Scenario: record.ScenarioRef{Name: "sso"},
			Params: map[string]string{
				"sso_password":  "Lab-sso-admin-1!",
				"api_token":     "tok-123",
				"client-secret": "shh",
				"PRIVATE_KEY":   "-----BEGIN",
				"version":       "1.2.3",
			},
		})
		emit(t, w, record.KindStepStarted, 1, record.StepStarted{Label: "log in", Host: "box"})
		secret := "hunter2"
		emit(t, w, record.KindFact, 1, record.Fact{Key: "admin.password", Value: &secret})
	})

	v, err := newDetailView(dir, func() {})
	if err != nil {
		t.Fatalf("newDetailView: %v", err)
	}
	shown := v.metaText() + "\n" + v.factsText()
	for _, secret := range []string{"Lab-sso-admin-1!", "tok-123", "shh", "-----BEGIN", "hunter2"} {
		if strings.Contains(shown, secret) {
			t.Errorf("%q is on screen:\n%s", secret, shown)
		}
	}
	for _, want := range []string{"sso_password=" + hidden, "version=1.2.3", "admin.password " + hidden} {
		if !strings.Contains(shown, want) {
			t.Errorf("want %q on screen:\n%s", want, shown)
		}
	}
}

func TestOnlyNamesThatSaySecretAreHidden(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"password", "sso_password", "DB_PASSWD", "api-key", "apikey", "token", "client_secret", "private_key", "credentials", "passphrase"} {
		if !looksSecret(name) {
			t.Errorf("%q not treated as secret", name)
		}
	}
	for _, name := range []string{"version", "channel", "from", "to", "host", "key_count", "keyboard"} {
		if looksSecret(name) {
			t.Errorf("%q treated as secret", name)
		}
	}
}
