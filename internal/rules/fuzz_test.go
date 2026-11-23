package rules

import (
	"strings"
	"testing"
)

func FuzzParseOptOut(f *testing.F) {
	for _, seed := range []string{
		"# june: ignore[PY012] shared on purpose",
		"# june: ignore[PY012]",
		"# june: ignore[PY012]reason",
		"# june: ignore[ PY012] reason",
		"# june: ignore[PY012,PY013] reason",
		"# june: ignore[NOPE] reason",
		"# june: ignore[",
		"# june:ignore[PY012] reason",
		"",
	} {
		f.Add(seed)
	}
	rs := &RuleSet{ids: map[string]bool{"PY012": true, "PY013": true}}
	f.Fuzz(func(t *testing.T, text string) {
		rule, reason, problem := rs.parseOptOut(text)
		if problem != "" {
			if reason != "" {
				t.Fatalf("parseOptOut(%q) refused with %q but still gave the reason %q", text, problem, reason)
			}
			return
		}
		if !rs.ids[rule] {
			t.Fatalf("parseOptOut(%q) honoured the unknown rule %q", text, rule)
		}
		if reason == "" || reason != strings.TrimSpace(reason) {
			t.Fatalf("parseOptOut(%q) honoured the reason %q, want a non-empty trimmed reason", text, reason)
		}
	})
}
