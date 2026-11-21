package diag_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/vicolby/june/internal/diag"
)

func TestRenderSortsFindings(t *testing.T) {
	ds := []diag.Diagnostic{
		{File: "b.py", Line: 2, Col: 1, Code: "PY012", Msg: "m"},
		{File: "a.py", Line: 10, Col: 3, Code: "PY012", Msg: "m"},
		{File: "a.py", Line: 9, Col: 8, Code: "PY013", Msg: "n"},
		{File: "a.py", Line: 9, Col: 2, Code: "PY012", Msg: "m"},
	}
	out, files := diag.Render(ds, "", diag.Text)
	want := "a.py:9:2: PY012 m\n" +
		"a.py:9:8: PY013 n\n" +
		"a.py:10:3: PY012 m\n" +
		"b.py:2:1: PY012 m\n"
	if string(out) != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
}

func TestRenderIsIndependentOfInputOrder(t *testing.T) {
	a := diag.Diagnostic{File: "a.py", Line: 1, Col: 1, Code: "PY012", Msg: "m"}
	b := diag.Diagnostic{File: "a.py", Line: 2, Col: 1, Code: "PY012", Msg: "m"}
	one, _ := diag.Render([]diag.Diagnostic{a, b}, "", diag.Text)
	two, _ := diag.Render([]diag.Diagnostic{b, a}, "", diag.Text)
	if string(one) != string(two) {
		t.Errorf("order changed output:\n%s\nvs\n%s", one, two)
	}
}

func TestRenderFailure(t *testing.T) {
	ds := []diag.Diagnostic{{Kind: diag.Failure, Msg: "cannot do it. Fix: do that"}}
	out, _ := diag.Render(ds, "", diag.Text)
	if got, want := string(out), "june: error: cannot do it. Fix: do that\n"; got != want {
		t.Errorf("out = %q, want %q", got, want)
	}
}

func TestRenderSummaryAndEmpty(t *testing.T) {
	out, _ := diag.Render(nil, "", diag.Text)
	if len(out) != 0 {
		t.Errorf("empty render = %q, want nothing", out)
	}
	out, _ = diag.Render(nil, "checked 1 file", diag.Text)
	if got, want := string(out), "checked 1 file\n"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestRenderHasNoANSI(t *testing.T) {
	ds := []diag.Diagnostic{
		{File: "a.py", Line: 1, Col: 1, Code: "PY012", Sev: diag.Error, Msg: "m"},
		{Kind: diag.Failure, Msg: "x. Fix: y"},
	}
	out, _ := diag.Render(ds, "", "")
	if strings.Contains(string(out), "\x1b") {
		t.Errorf("out contains an escape sequence: %q", out)
	}
}

func TestRenderEscapesControls(t *testing.T) {
	const text = "café \"quoted\" \\path \x00\x07\b\t\n\v\f\r\x1b[2K\x7f\u0085\u009b"
	const escaped = `café "quoted" \path \x00\a\b\t\n\v\f\r\x1b[2K\x7f\u0085\u009b`
	for _, tc := range []struct {
		name    string
		diags   []diag.Diagnostic
		summary string
		want    string
	}{
		{"finding", []diag.Diagnostic{{Kind: diag.Finding, File: text, Line: 1, Col: 2, Code: text, Msg: text}}, "", escaped + ":1:2: " + escaped + " " + escaped + "\n"},
		{"opt-out", []diag.Diagnostic{{Kind: diag.OptOut, File: text, Line: 1, Col: 2, Code: text, Msg: text}}, "", escaped + ":1:2: " + escaped + " " + escaped + "\n"},
		{"legacy", []diag.Diagnostic{{Kind: diag.Legacy, File: text, Line: 1, Col: 2, Code: text, Msg: text}}, "", "legacy: " + escaped + ":1:2: " + escaped + " " + escaped + "\n"},
		{"failure", []diag.Diagnostic{{Kind: diag.Failure, Msg: text}}, "", "june: error: " + escaped + "\n"},
		{"summary", nil, text, escaped + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := diag.Render(tc.diags, tc.summary, diag.Text)
			if string(out) != tc.want {
				t.Errorf("out = %q, want %q", out, tc.want)
			}
			for _, r := range string(out) {
				if unicode.IsControl(r) && r != '\n' {
					t.Errorf("out contains raw control %U: %q", r, out)
				}
			}
		})
	}
}

func TestRenderLegacyIsLabelledAndSortsAfterFindings(t *testing.T) {
	ds := []diag.Diagnostic{
		{Kind: diag.Legacy, File: "a.py", Line: 1, Col: 2, Code: "PY012", Sev: diag.Warning, Msg: "m"},
		{Kind: diag.Finding, File: "z.py", Line: 9, Col: 1, Code: "PY012", Sev: diag.Error, Msg: "m"},
		{Kind: diag.OptOut, File: "z.py", Line: 8, Col: 1, Code: "PY012", Sev: diag.Warning, Msg: "o"},
	}
	out, _ := diag.Render(ds, "", diag.Text)
	want := "z.py:8:1: PY012 o\nz.py:9:1: PY012 m\nlegacy: a.py:1:2: PY012 m\n"
	if string(out) != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
}
