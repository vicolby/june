package check_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
	"github.com/vicolby/june/internal/rules"
)

const py012 = "PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.\n"

// tree builds a temp tree with the fixture Python rules at .june/rules and the given files.
func tree(t *testing.T, files map[string]string) check.Env {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, filepath.Join("..", "..", "testdata", "rules", "python"), filepath.Join(dir, ".june", "rules"))
	for name, body := range files {
		write(t, filepath.Join(dir, name), body)
	}
	return check.Env{Dir: dir}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func change(t *testing.T, env check.Env, list string) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader(list)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var summaryLine = regexp.MustCompile(`^june: \d+ errors?, \d+ warnings?, \d+ opt-outs?; checked \d+ files?, \d+ changed lines?(; \d+ files? skipped, not readable as text)?(; base \S+ \([a-z -]+\))?\n$`)

// skippedText is the warning june prints when a Python file is left out of the scan.
func skippedText(file, reason string) string {
	return file + ":1:1: ENCODING file not scanned: " + reason + ". Fix: add a PEP 263 coding declaration for a supported encoding, or save it as UTF-8\n"
}

func wantSkippedSummary(t *testing.T, res check.Result, n int) {
	t.Helper()
	noun := "files"
	warn := "warnings"
	if n == 1 {
		noun = "file"
		warn = "warning"
	}
	sum := summaryOf(res)
	for _, want := range []string{
		fmt.Sprintf("%d %s skipped, not readable as text", n, noun),
		fmt.Sprintf("%d %s,", n, warn),
	} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary = %q, want it to contain %q", sum, want)
		}
	}
}

// expect compares the findings in Out; the summary line has its own tests, so here it only has to close the output.
func expect(t *testing.T, res check.Result, exit int, findings string) {
	t.Helper()
	if res.Exit != exit {
		t.Errorf("Exit = %d, want %d", res.Exit, exit)
	}
	if got := findingsOf(t, res); got != findings {
		t.Errorf("findings =\n%s\nwant\n%s", got, findings)
	}
}

// findingsOf returns Out without its summary line, failing if the summary is missing.
func findingsOf(t *testing.T, res check.Result) string {
	t.Helper()
	out := string(res.Out)
	i := strings.LastIndex(strings.TrimSuffix(out, "\n"), "\n") + 1
	if !summaryLine.MatchString(out[i:]) {
		t.Errorf("Out does not end with a summary line:\n%s", out)
		return out
	}
	return out[:i]
}

func summaryOf(res check.Result) string {
	out := strings.TrimSuffix(string(res.Out), "\n")
	return out[strings.LastIndex(out, "\n")+1:]
}

func TestMatchOnTouchedLineFails(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "import os\n\ndef f(a, b=[]):\n    return a\n"})
	expect(t, change(t, env, "app.py:3\n"), 1, "app.py:3:12: "+py012)
}

func TestRangeTouchesEveryLineInIt(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = 1\ndef f(a=[]):\n    pass\n"})
	expect(t, change(t, env, "app.py:1-2\n"), 1, "app.py:2:9: "+py012)
	expect(t, change(t, env, "app.py:2-2\n"), 1, "app.py:2:9: "+py012)
}

func TestMatchOnUntouchedLineNeverFails(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]):\n    pass\n\nx = 1\n"})
	expect(t, change(t, env, "app.py:3-4\n"), 0, "")
}

// Python ends a physical line at CR, LF, or CRLF
// (https://docs.python.org/3/reference/lexical_analysis.html#physical-lines).
// CRLF still shares LF with the scanner, so the violation stays on that line.
// A bare CR does not, and listing the Python line must name the file instead of passing clean.
func TestCRLFKeepsPythonLineNumbers(t *testing.T) {
	env := tree(t, map[string]string{
		"latin.py": "# coding: latin-1\r\nx = '\xe9'\r\ndef f(a=[]): pass\r\n",
		"utf.py":   "x = 1\r\ndef f(a=[]): pass\r\n",
	})
	expect(t, change(t, env, "latin.py:3\nutf.py:2\n"), 1, "latin.py:3:9: "+py012+"utf.py:2:9: "+py012)
	expect(t, change(t, env, "latin.py:2\nutf.py:1\n"), 0, "")
}

func TestBareCarriageReturnIsSkippedNotShifted(t *testing.T) {
	const reason = "it uses bare carriage returns as line endings"
	env := tree(t, map[string]string{
		"app.py": "# coding: latin-1\r# caf\xe9\r\ndef f(a=[]): pass\n",
		"utf.py": "x = 1\rdef f(a=[]): pass\n",
		"ok.py":  "def g(b=[]): pass\n",
	})
	res := change(t, env, "app.py:3\nutf.py:2\nok.py:1\n")
	expect(t, res, 1, skippedText("app.py", reason)+"ok.py:1:9: "+py012+skippedText("utf.py", reason))
	wantSkippedSummary(t, res, 2)
}

func TestMatchIsJudgedOnItsFirstLineOnly(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(\n    a=\n    [\n        1,\n        2,\n    ],\n):\n    pass\n"})
	expect(t, change(t, env, "app.py:4-6\n"), 0, "")
	expect(t, change(t, env, "app.py:2\n"), 0, "")
	expect(t, change(t, env, "app.py:3\n"), 1, "app.py:3:5: "+py012)
}

func TestOutputIsSortedAndPortable(t *testing.T) {
	env := tree(t, map[string]string{
		"z.py":     "def f(a=[]): pass\n",
		"pkg/a.py": "x = 1\ndef g(b={}): pass\ndef h(c=[]): pass\n",
	})
	res := change(t, env, "z.py:1\npkg/a.py:3\npkg/a.py:2\n")
	want := "pkg/a.py:2:9: " + py012 + "pkg/a.py:3:9: " + py012 + "z.py:1:9: " + py012
	expect(t, res, 1, want)
	if strings.Contains(string(res.Out), env.Dir) || strings.Contains(string(res.Out), "\x1b") {
		t.Errorf("Out has an absolute path or ANSI: %q", res.Out)
	}
	again := change(t, env, "pkg/a.py:2\npkg/a.py:3\nz.py:1\n")
	if string(again.Out) != string(res.Out) {
		t.Errorf("list order changed Out:\n%s\nvs\n%s", again.Out, res.Out)
	}
}

func TestOnlyPythonFilesAreRead(t *testing.T) {
	env := tree(t, map[string]string{"notes.md": "def f(a=[]): pass\n", "gone.txt": ""})
	expect(t, change(t, env, "notes.md:1\n"), 0, "")
}

func TestPathSpellingsAgree(t *testing.T) {
	env := tree(t, map[string]string{"pkg/app.py": "def f(a=[]): pass\n"})
	expect(t, change(t, env, "./pkg/../pkg/app.py:1\n"), 1, "pkg/app.py:1:9: "+py012)
}

func TestRulesFlagOverridesDefault(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	copyDir(t, filepath.Join("..", "..", "testdata", "rules", "python"), filepath.Join(env.Dir, "elsewhere"))
	res, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n"), Rules: []string{"elsewhere"}})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res, 1, "app.py:1:9: "+py012)
	if err := os.RemoveAll(filepath.Join(env.Dir, ".june")); err != nil {
		t.Fatal(err)
	}
	res, _ = check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n"), Rules: []string{"elsewhere"}})
	expect(t, res, 1, "app.py:1:9: "+py012)
}

func TestRuleSetExistsOnlyIfEveryExampleBehaved(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	// An example that claims a mutable default is valid.
	write(t, filepath.Join(env.Dir, ".june/rules/rule-tests/PY012-test.yml"),
		"id: PY012\nvalid:\n  - \"def f(a=[]): pass\"\ninvalid:\n  - \"def f(a=[]): pass\"\n")
	res := change(t, env, "app.py:1\n")
	if res.Exit != 2 {
		t.Fatalf("Exit = %d, want 2; Out: %s", res.Exit, res.Out)
	}
	out := string(res.Out)
	fix := ". Fix: make the rule and its examples in rule-tests/PY012-test.yml agree\n"
	want := "june: error: .june/rules/rule-tests/PY012-test.yml: rule PY012 valid example \"def f(a=[]): pass\" is reported by the rule" + fix
	if out != want {
		t.Errorf("Out = %q, want %q", out, want)
	}
	if strings.Contains(out, "app.py:") || strings.Contains(out, env.Dir) {
		t.Errorf("Out has a finding or an absolute path: %q", out)
	}
}

func TestRefusals(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		list  *string
		rules string
		path  string
		out   string
	}{
		{name: "not an entry", list: ptr("app.py\n"), out: "june: error: changed-lines line 1 is not valid: expected path:N or path:N-M. Fix: " + listFix + "\n"},
		{name: "line zero", list: ptr("app.py:0\n"), out: "june: error: changed-lines line 1 is not valid: expected a line number from 1. Fix: " + listFix + "\n"},
		{name: "reversed range", list: ptr("\napp.py:9-3\n"), out: "june: error: changed-lines line 2 is not valid: expected a line number from 9. Fix: " + listFix + "\n"},
		{name: "words", list: ptr("app.py:x\n"), out: "june: error: changed-lines line 1 is not valid: expected a line number from 1. Fix: " + listFix + "\n"},
		{name: "absolute path", list: ptr("/etc/app.py:1\n"), out: "june: error: changed-lines line 1 is not valid: the path is not inside the repository. Fix: " + listFix + "\n"},
		{name: "escaping path", list: ptr("../app.py:1\n"), out: "june: error: changed-lines line 1 is not valid: the path is not inside the repository. Fix: " + listFix + "\n"},
		{name: "missing file", list: ptr("nope.py:1\n"), out: "june: error: changed-lines names nope.py, which is not a file in the tree. Fix: regenerate the list from the current tree\n"},
		{name: "missing rules", list: ptr("app.py:1\n"), rules: "absent", out: "june: error: absent: the rules directory does not exist. Fix: create it or point --rules at an existing rules directory\n"},
		{name: "missing ast-grep", list: ptr("app.py:1\n"), path: "none", out: "june: error: .june/rules: ast-grep was not found. Fix: install ast-grep and put it on PATH, for example npm install --global @ast-grep/cli\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
			if tt.path != "" {
				t.Setenv("PATH", t.TempDir())
			}
			req := check.Request{}
			if tt.rules != "" {
				req.Rules = []string{tt.rules}
			}
			if tt.list != nil {
				req.Changed = strings.NewReader(*tt.list)
			}
			res, err := check.Change(context.Background(), env, req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Exit != 2 || string(res.Out) != tt.out {
				t.Errorf("Exit = %d, Out =\n%s\nwant 2 and\n%s", res.Exit, res.Out, tt.out)
			}
		})
	}
}

const listFix = "write one path:7 or path:7-12 per line, with paths relative to the repository root"

func ptr(s string) *string { return &s }

func TestCancelledContextIsAnError(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := check.Change(ctx, env, check.Request{Changed: strings.NewReader("app.py:1\n")}); !errors.Is(err, context.Canceled) {
		t.Errorf("Change error = %v, want context.Canceled", err)
	}
}

func TestCancellationAbandonsOpenChangedLines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }() // io.Pipe closes always return nil.
	defer func() { _ = writer.Close() }()
	env := check.Env{Dir: t.TempDir()}
	done := make(chan error, 1)
	go func() {
		_, err := check.Change(ctx, env, check.Request{Changed: reader})
		done <- err
	}()
	if _, err := io.WriteString(writer, "app.py:1\n"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Change error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Change stayed blocked on the open changed-lines reader after cancellation")
	}
}

func TestMutableDefaultForms(t *testing.T) {
	values := []string{"[]", "{}", "{1}", "[x for x in ()]", "{x: x for x in ()}", "{x for x in ()}"}
	for _, parameter := range []string{"def f(a=", "def f(a: object =", "f = (lambda a="} {
		for _, value := range values {
			for _, depth := range []int{0, 1, 3} {
				t.Run(fmt.Sprintf("%s%s/%d", parameter, value, depth), func(t *testing.T) {
					ending := "): pass\n"
					if strings.Contains(parameter, "lambda") {
						ending = ": a)\n"
					}
					source := parameter + strings.Repeat("(", depth) + value + strings.Repeat(")", depth) + ending
					env := tree(t, map[string]string{"app.py": source})
					column := len(parameter) + depth + 1
					expect(t, change(t, env, "app.py:1\n"), 1, fmt.Sprintf("app.py:1:%d: %s", column, py012))
				})
			}
		}
	}
}

func TestOnlyMutableDefaultValuesMatch(t *testing.T) {
	for _, value := range []string{"(None)", "((1))", "([],)", "tuple([])", "([] or None)", "([] if condition else None)", "(lambda: [])"} {
		env := tree(t, map[string]string{"app.py": "def f(a=" + value + "): pass\n"})
		expect(t, change(t, env, "app.py:1\n"), 0, "")
	}
}

func TestMultilineMutableValueAnchors(t *testing.T) {
	for _, parameter := range []string{"a", "a: list"} {
		env := tree(t, map[string]string{"app.py": "def f(\n    " + parameter + "=\n    ((\n        []\n    )),\n): pass\n"})
		expect(t, change(t, env, "app.py:2-3\n"), 0, "")
		expect(t, change(t, env, "app.py:4\n"), 1, "app.py:4:9: "+py012)
	}
	env := tree(t, map[string]string{"app.py": "f = (lambda a=\n    ((\n        []\n    )): a)\n"})
	expect(t, change(t, env, "app.py:1-2\n"), 0, "")
	expect(t, change(t, env, "app.py:3\n"), 1, "app.py:3:9: "+py012)
}

func TestNativeSuppressionsDoNotHideFindings(t *testing.T) {
	for _, directive := range []string{"ast-grep-ignore", "ast-grep-ignore: PY012", "ast-grep-ignore: OTHER, PY012"} {
		for _, tc := range []struct {
			source string
			line   int
		}{
			{"# " + directive + "\n\ndef f(a=[]): pass\n", 3},
			{"# " + directive + "\ndef f(a=[]): pass\n", 2},
			{"def f(a=[]): pass # " + directive + "\n", 1},
			{"# é " + directive + "\ndef f(a=[]): pass # " + directive + "\n", 2},
		} {
			env := tree(t, map[string]string{"app.py": tc.source})
			expect(t, change(t, env, fmt.Sprintf("app.py:%d\n", tc.line)), 1, fmt.Sprintf("app.py:%d:9: %s", tc.line, py012))
		}
	}
}

func TestChangedLinesPreservesPathWhitespace(t *testing.T) {
	env := tree(t, map[string]string{" app.py": "def f(a=[]): pass\n", "app.py": "x = 1\n", "\tapp.py": "def f(a=[]): pass\n"})
	for _, list := range []string{" app.py:1\n", "\n \t\n app.py:1-1\r\n"} {
		expect(t, change(t, env, list), 1, " app.py:1:9: "+py012)
	}
	expect(t, change(t, env, "\tapp.py:1\n"), 1, "\\tapp.py:1:9: "+py012)
	expect(t, change(t, env, "app.py:1\n"), 0, "")
}

func TestFailureOutputHasNoAbsolutePaths(t *testing.T) {
	for _, failure := range []string{"rules directory", "rule file", "reader", "scan", "temporary directory"} {
		t.Run(failure, func(t *testing.T) {
			env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
			req := check.Request{Changed: strings.NewReader("app.py:1\n")}
			switch failure {
			case "rules directory":
				req.Rules = []string{filepath.Join(env.Dir, "missing", "rules")}
			case "rule file":
				file := filepath.Join(env.Dir, ".june", "rules", "PY012.yml")
				if err := os.Chmod(file, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(file, 0o644); err != nil {
						t.Error(err)
					}
				})
			case "reader":
				req.Changed = iotest.ErrReader(&os.PathError{Op: "read", Path: filepath.Join(env.Dir, "changed.txt"), Err: os.ErrPermission})
			case "scan":
				file := filepath.Join(env.Dir, "app.py")
				if err := os.Chmod(file, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(file, 0o644); err != nil {
						t.Error(err)
					}
				})
			case "temporary directory":
				t.Setenv("TMPDIR", filepath.Join(env.Dir, "missing"))
			}
			res, err := check.Change(context.Background(), env, req)
			if err != nil || res.Exit != 2 {
				t.Fatalf("Change = %+v, %v, want refusal", res, err)
			}
			if strings.Contains(string(res.Out), env.Dir) || strings.Contains(string(res.Out), "\x1b") {
				t.Errorf("output exposes an absolute path or ANSI: %s", res.Out)
			}
		})
	}
}

const warnRule = `id: LST02
language: python
severity: warning
message: "Name the variable. Fix: rename it."
rule:
  pattern: tmp = $V
`

const warnExample = `id: LST02
valid:
  - "value = 1"
invalid:
  - "tmp = 1"
`

func withWarningRule(t *testing.T, env check.Env) check.Env {
	t.Helper()
	write(t, filepath.Join(env.Dir, ".june", "rules", "LST02.yml"), warnRule)
	write(t, filepath.Join(env.Dir, ".june", "rules", "rule-tests", "LST02-test.yml"), warnExample)
	return env
}

const lst02 = "LST02 Name the variable. Fix: rename it.\n"

func TestWarningOnTouchedLinePrintsAndExitsZero(t *testing.T) {
	env := withWarningRule(t, tree(t, map[string]string{"app.py": "x = 1\ntmp = 2\n"}))
	res := change(t, env, "app.py:2\n")
	expect(t, res, 0, "app.py:2:1: "+lst02)
	if got, want := summaryOf(res), "june: 0 errors, 1 warning, 0 opt-outs; checked 1 file, 1 changed line"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestSkippedFileCountsAsAWarningAlongsideRuleWarnings(t *testing.T) {
	env := withWarningRule(t, tree(t, map[string]string{
		"app.py": "tmp = 2\n",
		"bad.py": "x = '\xe9'\n",
	}))
	res := change(t, env, "app.py:1\nbad.py:1\n")
	expect(t, res, 0, "app.py:1:1: "+lst02+skippedText("bad.py", "it is not valid UTF-8 and declares no other encoding"))
	if got, want := summaryOf(res), "june: 0 errors, 2 warnings, 0 opt-outs; checked 1 file, 1 changed line; 1 file skipped, not readable as text"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestSeverityComesFromTheRule(t *testing.T) {
	env := withWarningRule(t, tree(t, map[string]string{"app.py": "tmp = 1\ndef f(a=[]): pass\n"}))
	res := change(t, env, "app.py:1-2\n")
	expect(t, res, 1, "app.py:1:1: "+lst02+"app.py:2:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 1 warning, 0 opt-outs; checked 1 file, 2 changed lines"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}

	// Raising the same rule to an error is the only thing that makes it fail.
	write(t, filepath.Join(env.Dir, ".june", "rules", "LST02.yml"), strings.Replace(warnRule, "warning", "error", 1))
	res = change(t, env, "app.py:1\n")
	expect(t, res, 1, "app.py:1:1: "+lst02)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestSummaryCountsCoverage(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "x = 1\ny = 2\nz = 3\n", "b.py": "x = 1\n", "notes.md": "text\n"})
	cases := []struct{ list, want string }{
		{"", "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines"},
		{"a.py:1-2\na.py:2-3\na.py:3\n", "june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 3 changed lines"},
		{"a.py:1\nb.py:1\nnotes.md:1\n", "june: 0 errors, 0 warnings, 0 opt-outs; checked 2 files, 2 changed lines"},
	}
	for _, tc := range cases {
		res := change(t, env, tc.list)
		expect(t, res, 0, "")
		if got := summaryOf(res); got != tc.want {
			t.Errorf("list %q: summary = %q, want %q", tc.list, got, tc.want)
		}
	}
}

func TestSummaryIsTheLastLineAndDoesNotChangeFindings(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "def f(a=[]): pass\n", "b.py": "def g(b={}): pass\n"})
	res := change(t, env, "a.py:1\nb.py:1\n")
	lines := strings.Split(strings.TrimSuffix(string(res.Out), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "june: 2 errors") {
		t.Fatalf("Out = %q, want two findings then the summary", res.Out)
	}
	if res.Exit != 1 || len(res.Diags) != 2 {
		t.Errorf("Exit = %d, Diags = %d, want 1 and 2", res.Exit, len(res.Diags))
	}
}

func TestRefusalHasNoSummary(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = 1\n"})
	res := change(t, env, "app.py\n")
	if res.Exit != 2 || strings.Contains(string(res.Out), "checked") {
		t.Errorf("Exit = %d, Out = %q, want a refusal with no summary", res.Exit, res.Out)
	}
}

func TestOptOuts(t *testing.T) {
	const body = `def a(x=[]):  # june: ignore[PY012] shared on purpose
    pass
def b(x=[]):  # june: ignore[PY012]
    pass
def c(x=[]):  # june: ignore[PY999] typo
    pass
def d(x=1):  # june: ignore[PY012] nothing here
    pass
def e(x=[]):  # june: ignore PY012 why
    pass
def f(x="# june: ignore[PY012] in a string", y=[]):
    pass
def g(x=[]):  # june: ignore[PY012] untouched line
    pass
`
	env := tree(t, map[string]string{"a.py": body})
	res := change(t, env, "a.py:1-12\n")
	expect(t, res, 1, "a.py:1:15: PY012 opt-out: shared on purpose\n"+
		"a.py:3:15: OPTOUT malformed opt-out suppresses nothing: the reason is required. Fix: write # june: ignore[RULE] reason\n"+
		"a.py:5:15: OPTOUT malformed opt-out suppresses nothing: rule PY999 does not exist. Fix: name a rule from the rules directory\n"+
		"a.py:7:14: PY012 opt-out: nothing here (suppresses nothing: no PY012 finding on this line)\n"+
		"a.py:9:15: OPTOUT malformed opt-out suppresses nothing: the comment must start with `# june: ignore[`. Fix: write # june: ignore[RULE] reason\n"+
		"a.py:3:9: "+py012+
		"a.py:5:9: "+py012+
		"a.py:9:9: "+py012+
		"a.py:11:48: "+py012)
}

func TestOptOutHonouredKeepsExitZero(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "def a(x=[]):  # june: ignore[PY012] shared on purpose\n    pass\n"})
	expect(t, change(t, env, "a.py:1\n"), 0, "a.py:1:15: PY012 opt-out: shared on purpose\n")
}

func TestOptOutControlCharactersAreEscaped(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "def a(x=[]): pass # june: ignore[PY012] \x1b[2K\x1b[1Gapproved\n" +
		"def b(x=1): pass # june: ignore[PY012] café \"quoted\" \\path \x1b[2Kunmatched\n" +
		"def c(x=[]): pass # june: ignore[NO\x1b[2KPE] typo\n"})
	res := change(t, env, "a.py:1-3\n")
	expect(t, res, 1, "a.py:1:19: PY012 opt-out: \\x1b[2K\\x1b[1Gapproved\n"+
		"a.py:2:18: PY012 opt-out: café \"quoted\" \\path \\x1b[2Kunmatched (suppresses nothing: no PY012 finding on this line)\n"+
		"a.py:3:19: OPTOUT malformed opt-out suppresses nothing: rule NO\\x1b[2KPE does not exist. Fix: name a rule from the rules directory\n"+
		"a.py:3:9: "+py012)
	for _, r := range string(res.Out) {
		if unicode.IsControl(r) && r != '\n' {
			t.Errorf("output contains raw control %U: %q", r, res.Out)
		}
	}
}

func TestOptOutNearMissesSuppressNothing(t *testing.T) {
	const prefixProblem = "the comment must start with `# june: ignore[`"
	const paddingProblem = "the rule must not have surrounding whitespace"
	const reasonSpaceProblem = "a space is required before the reason"
	for _, tc := range []struct {
		comment string
		problem string
	}{
		{"#june: ignore[PY012] reason", prefixProblem},
		{"# june:ignore[PY012] reason", prefixProblem},
		{"#june:ignore[ PY012 ]why", prefixProblem},
		{"#\tjune: ignore[PY012] reason", prefixProblem},
		{"# june:\tignore[PY012] reason", prefixProblem},
		{"#  june: ignore[PY012] reason", prefixProblem},
		{"# june:  ignore[PY012] reason", prefixProblem},
		{"# june: ignore[ PY012] reason", paddingProblem},
		{"# june: ignore[PY012 ] reason", paddingProblem},
		{"# june: ignore[\tPY012\t] reason", paddingProblem},
		{"# june: ignore[PY012]reason", reasonSpaceProblem},
		{"# june: ignore[PY012]\treason", reasonSpaceProblem},
		{"# june: ignore[PY012] \t", "the reason is required"},
	} {
		for _, value := range []string{"[]", "1"} {
			t.Run(tc.comment+"/"+value, func(t *testing.T) {
				prefix := "def f(x=" + value + "): pass  "
				env := tree(t, map[string]string{"a.py": prefix + tc.comment + "\nx = 1\n"})
				out := fmt.Sprintf("a.py:1:%d: OPTOUT malformed opt-out suppresses nothing: %s. Fix: write # june: ignore[RULE] reason\n", len(prefix)+1, tc.problem)
				exit := 0
				if value == "[]" {
					exit = 1
					out += "a.py:1:9: " + py012
				}
				expect(t, change(t, env, "a.py:1\n"), exit, out)
				expect(t, change(t, env, "a.py:2\n"), 0, "")
			})
		}
	}
}

func TestOptOutOffTouchedLineIsSilent(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "def a(x=[]):  # june: ignore[PY012] why\n    pass\n"})
	expect(t, change(t, env, "a.py:2\n"), 0, "")
}

func TestSummaryCountsOptOutsOnTouchedLines(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "def a(x=[]):  # june: ignore[PY012] why\n    pass\ndef b(x=1):  # june: ignore[PY012]\n    pass\ndef c(x=1):  # june: ignore[PY012] untouched\n"})
	res := change(t, env, "a.py:1-4\n")
	if got, want := summaryOf(res), "june: 0 errors, 0 warnings, 2 opt-outs; checked 1 file, 4 changed lines"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// astGrepVersion is the version of the ast-grep on PATH, as june prints it.
func astGrepVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("ast-grep", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "ast-grep ")
}

func TestRulesVerifiesEveryExample(t *testing.T) {
	env := tree(t, nil)
	res, err := check.Rules(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "june: verified 5 rules, 135 examples with ast-grep " + astGrepVersion(t) + "\n"; res.Exit != 0 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out = %q, want 0 and %q", res.Exit, res.Out, want)
	}
}

func TestRulesFaultsRefuseVerifyAndRunAlike(t *testing.T) {
	rule := func(msg string) string {
		return "id: LST01\nlanguage: python\nseverity: error\nmessage: " + msg + "\nrule:\n  kind: list\n"
	}
	const sidecar = "rule-tests/LST01-test.yml"
	const goodExamples = "id: LST01\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n"
	tests := []struct {
		name  string
		files map[string]string
		out   string
	}{
		{
			name:  "unparseable rule",
			files: map[string]string{"LST01.yml": "id: LST01\nlanguage: python\nrule: [\n", sidecar: goodExamples},
			out:   "june: error: rules/LST01.yml: the rule must be one valid YAML document. Fix: keep exactly one valid YAML document in the rule file\n",
		},
		{
			name:  "rule ast-grep rejects",
			files: map[string]string{"LST01.yml": "id: LST01\nlanguage: python\nmessage: m\nrule:\n  kind: list\nbogus: 1\n", sidecar: goodExamples},
			out:   "june: error: rules/LST01.yml: ast-grep cannot parse the rule (",
		},
		{
			name:  "empty message",
			files: map[string]string{"LST01.yml": rule(`""`), sidecar: goodExamples},
			out:   "june: error: rules/LST01.yml: rule LST01 has an empty message. Fix: write the corrective instruction in the message\n",
		},
		{
			name:  "failing example",
			files: map[string]string{"LST01.yml": rule("m"), sidecar: "id: LST01\nvalid:\n  - \"x = [1]\"\ninvalid:\n  - \"x = (1,)\"\n"},
			out: "june: error: rules/rule-tests/LST01-test.yml: rule LST01 invalid example \"x = (1,)\" is not reported by the rule. Fix: make the rule and its examples in rule-tests/LST01-test.yml agree\n" +
				"june: error: rules/rule-tests/LST01-test.yml: rule LST01 valid example \"x = [1]\" is reported by the rule. Fix: make the rule and its examples in rule-tests/LST01-test.yml agree\n",
		},
		{
			name:  "zero rules",
			files: map[string]string{"README.md": "no rules here\n"},
			out:   "june: error: rules: the rules directory holds no rules. Fix: add a rule file such as NAME.yml with its rule-tests/NAME-test.yml, or point --rules elsewhere\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tt.files {
				write(t, filepath.Join(dir, "rules", name), body)
			}
			write(t, filepath.Join(dir, "app.py"), "x = [1]\n")
			env := check.Env{Dir: dir}

			verified, err := check.Rules(context.Background(), env, []string{"rules"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := check.Change(context.Background(), env, check.Request{Rules: []string{"rules"}, Changed: strings.NewReader("app.py:1\n")})
			if err != nil {
				t.Fatal(err)
			}
			if verified.Exit != 2 || run.Exit != 2 || string(verified.Out) != string(run.Out) {
				t.Fatalf("verify = %d %q, run = %d %q, want the same refusal with exit 2", verified.Exit, verified.Out, run.Exit, run.Out)
			}
			out := string(verified.Out)
			if !strings.HasPrefix(out, tt.out) || strings.Contains(out, "app.py:") || strings.Contains(out, dir) {
				t.Errorf("Out = %q, want prefix %q and no finding or absolute path", out, tt.out)
			}
		})
	}
}

func TestRulesCancelledContextIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := check.Rules(ctx, tree(t, nil), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRulesEmptyEnvDirIsAnError(t *testing.T) {
	if _, err := check.Rules(context.Background(), check.Env{}, nil); err == nil {
		t.Error("err = nil, want an error")
	}
}

// lst01 writes a rule that flags list displays, with examples, as id in dir.
func lst01(t *testing.T, dir, file, id, msg string) {
	t.Helper()
	write(t, filepath.Join(dir, file), "id: "+id+"\nlanguage: python\nseverity: error\nmessage: "+msg+"\nrule:\n  kind: list\n")
	write(t, filepath.Join(dir, "rule-tests", id+"-test.yml"), "id: "+id+"\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n")
}

func changeWith(t *testing.T, env check.Env, rules []string, list string) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, check.Request{Rules: rules, Changed: strings.NewReader(list)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRepeatedRulesAreMerged(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a={}): pass\nx = [1]\n"})
	lst01(t, filepath.Join(env.Dir, "shared"), "LST01.yml", "LST01", "No lists.")
	res := changeWith(t, env, []string{".june/rules", "shared"}, "app.py:1-2\n")
	want := "app.py:1:9: " + py012 + "app.py:2:5: LST01 No lists.\n"
	if res.Exit != 1 || !strings.HasPrefix(string(res.Out), want) {
		t.Errorf("Exit = %d, Out = %q, want 1 and prefix %q", res.Exit, res.Out, want)
	}
}

// The verdict does not depend on which directory comes first.
func TestRulesDirectoryOrderDoesNotChangeOutput(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a={}): pass\nx = [1]\n"})
	lst01(t, filepath.Join(env.Dir, "shared"), "LST01.yml", "LST01", "No lists.")
	a := changeWith(t, env, []string{".june/rules", "shared"}, "app.py:1-2\n")
	b := changeWith(t, env, []string{"shared", ".june/rules"}, "app.py:1-2\n")
	if a.Exit != 1 || b.Exit != 1 || string(a.Out) != string(b.Out) {
		t.Errorf("orders differ:\n%d %q\n%d %q", a.Exit, a.Out, b.Exit, b.Out)
	}
}

func TestRulesAcrossDirectoriesNameBothFilesOnDuplicateID(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]\n"})
	lst01(t, filepath.Join(env.Dir, "one"), "LST01.yml", "LST01", "No lists.")
	lst01(t, filepath.Join(env.Dir, "two"), "other.yml", "LST01", "Still no lists.")
	want := "june: error: two/other.yml: the rule id LST01 is duplicated (also defined in one/LST01.yml). Fix: give every rule a unique id\n"
	res := changeWith(t, env, []string{"one", "two"}, "app.py:1\n")
	if res.Exit != 2 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out = %q, want 2 and %q", res.Exit, res.Out, want)
	}
	verified, err := check.Rules(context.Background(), env, []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Exit != 2 || string(verified.Out) != want {
		t.Errorf("verify Exit = %d, Out = %q, want 2 and %q", verified.Exit, verified.Out, want)
	}
}

func TestCaseVariantRuleIDsAreDuplicates(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]\n"})
	// Foo's valid example is a list the rule matches, so skipping its examples would let the rule emit findings.
	write(t, filepath.Join(env.Dir, "A", "Foo.yml"), "id: Foo\nlanguage: python\nseverity: error\nmessage: No lists.\nrule:\n  kind: list\n")
	write(t, filepath.Join(env.Dir, "A", "rule-tests", "Foo-test.yml"), "id: Foo\nvalid:\n  - \"x = [1]\"\ninvalid:\n  - \"x = [1]\"\n")
	lst01(t, filepath.Join(env.Dir, "B"), "foo.yml", "foo", "No lists.")
	for _, tc := range []struct {
		dirs []string
		want string
	}{
		{[]string{"A", "B"}, "june: error: B/foo.yml: the rule id foo is duplicated (also defined in A/Foo.yml). Fix: give every rule a unique id\n"},
		{[]string{"B", "A"}, "june: error: A/Foo.yml: the rule id Foo is duplicated (also defined in B/foo.yml). Fix: give every rule a unique id\n"},
	} {
		res := changeWith(t, env, tc.dirs, "app.py:1\n")
		if res.Exit != 2 || string(res.Out) != tc.want {
			t.Errorf("dirs %v: Exit = %d, Out = %q, want 2 and %q", tc.dirs, res.Exit, res.Out, tc.want)
		}
		verified, err := check.Rules(context.Background(), env, tc.dirs)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Exit != 2 || string(verified.Out) != tc.want {
			t.Errorf("dirs %v: verify Exit = %d, Out = %q, want 2 and %q", tc.dirs, verified.Exit, verified.Out, tc.want)
		}
	}
}

func TestAbsoluteRulesDirectoriesWithTheSameBaseNameStayDistinct(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(app, "app.py"), "x = [1]\n")
	env := check.Env{Dir: app}
	shared := filepath.Join(root, "shared", "rules")
	local := filepath.Join(app, ".june", "rules")
	lst01(t, shared, "LST01.yml", "LST01", "No lists.")
	lst01(t, local, "LST01.yml", "LST01", "Still no lists.")
	want := "june: error: .june/rules/LST01.yml: the rule id LST01 is duplicated (also defined in ../shared/rules/LST01.yml). Fix: give every rule a unique id\n"
	res := changeWith(t, env, []string{shared, local}, "app.py:1\n")
	if res.Exit != 2 || string(res.Out) != want || strings.Contains(string(res.Out), root) {
		t.Errorf("Exit = %d, Out = %q, want 2 and %q", res.Exit, res.Out, want)
	}
	verified, err := check.Rules(context.Background(), env, []string{shared, local})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Exit != 2 || string(verified.Out) != want || strings.Contains(string(verified.Out), root) {
		t.Errorf("verify Exit = %d, Out = %q, want 2 and %q", verified.Exit, verified.Out, want)
	}
}

// Same file names in different directories with different ids must not collide while examples run.
func TestRulesAcrossDirectoriesMayShareFileNames(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]\ny = {1: 2}\n"})
	lst01(t, filepath.Join(env.Dir, "one"), "rule.yml", "LST01", "No lists.")
	write(t, filepath.Join(env.Dir, "two", "rule.yml"), "id: DCT01\nlanguage: python\nseverity: error\nmessage: No dicts.\nrule:\n  kind: dictionary\n")
	write(t, filepath.Join(env.Dir, "two", "rule-tests", "DCT01-test.yml"), "id: DCT01\nvalid:\n  - \"x = 1\"\ninvalid:\n  - \"x = {1: 2}\"\n")
	res := changeWith(t, env, []string{"one", "two"}, "app.py:1-2\n")
	if res.Exit != 1 || !strings.HasPrefix(string(res.Out), "app.py:1:5: LST01 No lists.\napp.py:2:5: DCT01 No dicts.\n") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestRulesRepeatedSameDirectoryCountsOnce(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	res := changeWith(t, env, []string{".june/rules", "./.june/rules", ".june/../.june/rules"}, "app.py:1\n")
	expect(t, res, 1, "app.py:1:9: "+py012)
	verified, err := check.Rules(context.Background(), env, []string{".june/rules", ".june/rules/"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "june: verified 5 rules, 135 examples with ast-grep " + astGrepVersion(t) + "\n"; verified.Exit != 0 || string(verified.Out) != want {
		t.Errorf("Exit = %d, Out = %q, want 0 and %q", verified.Exit, verified.Out, want)
	}
}

func TestRulesCountsSumAcrossDirectories(t *testing.T) {
	env := tree(t, nil)
	lst01(t, filepath.Join(env.Dir, "shared"), "LST01.yml", "LST01", "No lists.")
	res, err := check.Rules(context.Background(), env, []string{".june/rules", "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "june: verified 6 rules, 137 examples with ast-grep " + astGrepVersion(t) + "\n"; res.Exit != 0 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out = %q, want 0 and %q", res.Exit, res.Out, want)
	}
}

// Every bad directory is reported, each under its own spelling, and no partial set runs.
func TestRulesAcrossDirectoriesReportEveryBadDirectory(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]\n"})
	write(t, filepath.Join(env.Dir, "hollow", "README.md"), "no rules\n")
	res := changeWith(t, env, []string{".june/rules", "absent", "hollow"}, "app.py:1\n")
	want := "june: error: absent: the rules directory does not exist. Fix: create it or point --rules at an existing rules directory\n" +
		"june: error: hollow: the rules directory holds no rules. Fix: add a rule file such as NAME.yml with its rule-tests/NAME-test.yml, or point --rules elsewhere\n"
	if res.Exit != 2 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out = %q, want 2 and %q", res.Exit, res.Out, want)
	}
}

func TestFailingExampleNamesItsOwnDirectory(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]\n"})
	lst01(t, filepath.Join(env.Dir, "shared"), "LST01.yml", "LST01", "No lists.")
	write(t, filepath.Join(env.Dir, "shared", "rule-tests", "LST01-test.yml"), "id: LST01\nvalid:\n  - \"x = [1]\"\ninvalid:\n  - \"x = [1]\"\n")
	res := changeWith(t, env, []string{".june/rules", "shared"}, "app.py:1\n")
	want := "june: error: shared/rule-tests/LST01-test.yml: rule LST01 valid example \"x = [1]\" is reported by the rule. Fix: make the rule and its examples in rule-tests/LST01-test.yml agree\n"
	if res.Exit != 2 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out = %q, want 2 and %q", res.Exit, res.Out, want)
	}
}

func TestUnparseableRuleNamesItsOwnDirectory(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]\n"})
	write(t, filepath.Join(env.Dir, "shared", "rule.yml"), "id: BAD01\nlanguage: python\nmessage: m\nrule:\n  kind: list\nbogus: 1\n")
	write(t, filepath.Join(env.Dir, "shared", "rule-tests", "BAD01-test.yml"), "id: BAD01\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n")
	res := changeWith(t, env, []string{".june/rules", "shared"}, "app.py:1\n")
	if res.Exit != 2 || !strings.HasPrefix(string(res.Out), "june: error: shared/rule.yml: ast-grep cannot parse the rule (") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestEmptyRulesEntryMeansDefault(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	expect(t, changeWith(t, env, []string{""}, "app.py:1\n"), 1, "app.py:1:9: "+py012)
}

func TestOptOutNamesRuleFromAnyDirectory(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = [1]  # june: ignore[LST01] fixture\n"})
	lst01(t, filepath.Join(env.Dir, "shared"), "LST01.yml", "LST01", "No lists.")
	res := changeWith(t, env, []string{".june/rules", "shared"}, "app.py:1\n")
	if res.Exit != 0 || !strings.Contains(string(res.Out), "opt-out: fixture") {
		t.Errorf("Exit = %d, Out = %q, want 0 with the opt-out printed", res.Exit, res.Out)
	}
}

func changeLegacy(t *testing.T, env check.Env, list string) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader(list), Legacy: true})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

const legacyPY012 = "legacy: "

func TestLegacyIsOffByDefault(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]):\n    pass\n\nx = 1\n"})
	res := change(t, env, "app.py:4\n")
	expect(t, res, 0, "")
	if strings.Contains(string(res.Out), "legacy") {
		t.Errorf("Out mentions legacy without --legacy: %q", res.Out)
	}
}

func TestLegacyListsUntouchedViolationsAndNeverFails(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]):\n    pass\n\nx = 1\n"})
	res := changeLegacy(t, env, "app.py:4\n")
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0", res.Exit)
	}
	if got, want := string(res.Out), legacyPY012+"app.py:1:9: "+py012+"june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; 1 legacy violation listed\n"; got != want {
		t.Errorf("Out =\n%s\nwant\n%s", got, want)
	}
}

func TestLegacyFollowsFindingsAndDoesNotRepeatThem(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def g(b={}): pass\ndef f(a=[]): pass\n"})
	res := changeLegacy(t, env, "app.py:2\n")
	want := "app.py:2:9: " + py012 + legacyPY012 + "app.py:1:9: " + py012 +
		"june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; 1 legacy violation listed\n"
	if res.Exit != 1 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out =\n%s\nwant 1 and\n%s", res.Exit, res.Out, want)
	}
}

func TestLegacyWarningsAndErrorsAreBothInformation(t *testing.T) {
	env := withWarningRule(t, tree(t, map[string]string{"app.py": "tmp = 1\ndef f(a=[]): pass\nx = 1\n"}))
	res := changeLegacy(t, env, "app.py:3\n")
	want := legacyPY012 + "app.py:1:1: " + lst02 + legacyPY012 + "app.py:2:9: " + py012 +
		"june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; 2 legacy violations listed\n"
	if res.Exit != 0 || string(res.Out) != want {
		t.Errorf("Exit = %d, Out =\n%s\nwant 0 and\n%s", res.Exit, res.Out, want)
	}
}

func TestLegacyOnlyScansFilesInTheChange(t *testing.T) {
	env := tree(t, map[string]string{"a.py": "x = 1\n", "other.py": "def f(a=[]): pass\n"})
	res := changeLegacy(t, env, "a.py:1\n")
	if res.Exit != 0 || strings.Contains(string(res.Out), "other.py") || strings.Contains(string(res.Out), "legacy") {
		t.Errorf("Exit = %d, Out = %q, want no legacy lines for an unchanged file", res.Exit, res.Out)
	}
}

func TestLegacyWithNothingToListKeepsSummaryUnchanged(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = 1\n"})
	res := changeLegacy(t, env, "app.py:1\n")
	expect(t, res, 0, "")
}

func TestLegacyHonoursValidOptOutOnUntouchedLine(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass  # june: ignore[PY012] shared on purpose\nx = 1\n"})
	res := changeLegacy(t, env, "app.py:2\n")
	expect(t, res, 0, "")
}

func TestLegacyDiagnosticsAreMarkedLegacy(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\nx = 1\n"})
	res := changeLegacy(t, env, "app.py:2\n")
	if len(res.Diags) != 1 || res.Diags[0].Kind != diag.Legacy || res.Diags[0].Sev == diag.Error {
		t.Errorf("Diags = %+v, want one legacy diagnostic that is not an error", res.Diags)
	}
}

func TestLegacyDoesNotMaskRefusals(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = 1\n"})
	res := changeLegacy(t, env, "app.py\n")
	if res.Exit != 2 {
		t.Errorf("Exit = %d, want 2", res.Exit)
	}
}

// An ast-grep below the floor is refused by verify and by a run, with the same fix, before any scan.
func TestOldASTGrepIsRefused(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	bin := t.TempDir()
	script := filepath.Join(bin, "ast-grep")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'ast-grep 0.40.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	want := "june: error: .june/rules: ast-grep 0.40.0 is older than the oldest supported version " + rules.MinVersion +
		". Fix: install ast-grep " + rules.PinnedVersion + " or newer, for example npm install --global @ast-grep/cli@" + rules.PinnedVersion + "\n"
	verified, err := check.Rules(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n")})
	if err != nil {
		t.Fatal(err)
	}
	for name, res := range map[string]check.Result{"verify": verified, "run": run} {
		if res.Exit != 2 || string(res.Out) != want {
			t.Errorf("%s: Exit = %d, Out = %q, want 2 and %q", name, res.Exit, res.Out, want)
		}
	}
}

func TestASTGrepDescription(t *testing.T) {
	if got, want := check.ASTGrep(context.Background()), "ast-grep "+astGrepVersion(t); got != want {
		t.Errorf("ASTGrep = %q, want %q", got, want)
	}
	for name, tt := range map[string]struct{ script, want string }{
		"old":     {"echo 'ast-grep 0.40.0'", "ast-grep 0.40.0 (unsupported: june needs " + rules.MinVersion + " or newer)"},
		"garbage": {"echo hi", `ast-grep version unreadable (ast-grep --version printed "hi")`},
	} {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "ast-grep"), []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		if got := check.ASTGrep(context.Background()); got != tt.want {
			t.Errorf("%s: ASTGrep = %q, want %q", name, got, tt.want)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if got, want := check.ASTGrep(context.Background()), "ast-grep not found on PATH"; got != want {
		t.Errorf("missing: ASTGrep = %q, want %q", got, want)
	}
}
