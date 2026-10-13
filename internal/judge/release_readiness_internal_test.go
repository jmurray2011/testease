package judge

import "testing"

func TestSchemaMatchesTargetFollowsTheReleaseLine(t *testing.T) {
	// Flyway numbers migrations by release line, so a four-part product
	// version such as 7.3.2.1 lands on schema 7.3.2.11: the eleventh migration
	// of the 7.3.2 line, not a suffix of "7.3.2.1".
	cases := []struct {
		schema, target string
		want           bool
	}{
		{"7.2.3.3", "7.2.3", true},
		{"7.2.3", "7.2.3", true},
		{"7.3.2.11", "7.3.2.1", true},
		{"7.3.2.1", "7.3.2.1", true},
		{"7.3.2.0", "7.3.2.1", false},
		{"7.2.3.3", "7.3.2.1", false},
		{"6.2.2.1", "7.3.2.1", false},
		{"6.2.2.1", "7.2.3", false},
		{"5.0", "5.0.5", false},
		{"", "7.3.2.1", false},
		{"unknown", "7.3.2.1", false},
	}
	for _, c := range cases {
		if got := schemaMatchesTarget(c.schema, c.target); got != c.want {
			t.Errorf("schemaMatchesTarget(%q, %q) = %v, want %v", c.schema, c.target, got, c.want)
		}
	}
}
