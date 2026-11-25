package review_test

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/vicolby/june/internal/diag"
	"github.com/vicolby/june/internal/review"
	"github.com/vicolby/june/internal/review/reviewtest"
)

// Whatever a model answers, a finding is a warning on a touched line of a file of the call, with a one-line reason.
func FuzzAnswer(f *testing.F) {
	for _, seed := range []string{
		`{"findings": []}`,
		`{"findings": [{"path": "a.py", "line": 2, "reason": "r"}]}`,
		`{"findings": [{"path": "a.py", "line": 9, "reason": "r"}]}`,
		`{"findings": [{"path": "../x", "line": 2, "reason": "r\n\u001b[0m"}]}`,
		`{"findings": null}`,
		`not json`,
		``,
	} {
		f.Add(seed)
	}
	dir := f.TempDir()
	put(f, dir+"/rules.md", rulesText)
	plan, faults := review.Prepare(dir, review.Config{Rules: "rules.md", Model: "m"})
	if len(faults) > 0 {
		f.Fatal(faults)
	}
	src := review.Source{Path: "a.py", Text: lines(8), Touched: []review.Span{{From: 2, To: 3}}}
	f.Fuzz(func(t *testing.T, answer string) {
		out, err := plan.Run(context.Background(), reviewtest.NewFake(reviewtest.Text(answer)), []review.Source{src})
		if err != nil {
			t.Fatalf("Run(%q): %v", answer, err)
		}
		for _, d := range out.Diags {
			if d.Sev != diag.Warning {
				t.Fatalf("answer %q made a non-warning: %+v", answer, d)
			}
			if d.Kind != diag.Finding {
				continue
			}
			if d.File != "a.py" || d.Line < 2 || d.Line > 3 {
				t.Fatalf("answer %q made a finding off the touched lines: %+v", answer, d)
			}
			if d.Msg == "" || len([]rune(d.Msg)) > 300 || strings.IndexFunc(d.Msg, unicode.IsControl) >= 0 {
				t.Fatalf("answer %q made an unclean reason %q", answer, d.Msg)
			}
		}
	})
}
