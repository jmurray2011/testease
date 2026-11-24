package main

import "strings"

// hidden stands in for a value this app will not put on screen.
const hidden = "•••••• (hidden)"

// secretWords end the words of a name whose value is a credential. A name is
// split on its separators first, so "ssopassword" is caught and "key_count" is
// not a key.
var secretWords = []string{"password", "passwd", "passphrase", "secret", "token", "credential", "credentials", "apikey"}

// secretPairs are two-word names that are secret together and not apart.
var secretPairs = [][2]string{{"api", "key"}, {"private", "key"}, {"access", "key"}}

// looksSecret says whether a param or fact name holds a credential. The record
// keeps the value either way - it is evidence and never rewritten - but this
// app does not show it, because a screen is read over shoulders and in
// screenshots. The name stays, so a person can see there was one.
func looksSecret(name string) bool {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' '
	})
	for i, w := range words {
		for _, s := range secretWords {
			if strings.HasSuffix(w, s) {
				return true
			}
		}
		if i+1 < len(words) {
			for _, p := range secretPairs {
				if w == p[0] && words[i+1] == p[1] {
					return true
				}
			}
		}
	}
	return false
}

// shown is a value as this app shows it.
func shown(name, value string) string {
	if looksSecret(name) {
		return hidden
	}
	return value
}
