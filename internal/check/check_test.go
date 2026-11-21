package check_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/vicolby/june/internal/check"
)

const py012 = "PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.\n"

// tree builds a temp tree with the shipped Python rules at .june/rules and the given files.
func tree(t *testing.T, files map[string]string) check.Env {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, filepath.Join("..", "..", "rules", "python"), filepath.Join(dir, ".june", "rules"))
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

func expect(t *testing.T, res check.Result, exit int, out string) {
	t.Helper()
	if res.Exit != exit {
		t.Errorf("Exit = %d, want %d", res.Exit, exit)
	}
	if string(res.Out) != out {
		t.Errorf("Out =\n%s\nwant\n%s", res.Out, out)
	}
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
	copyDir(t, filepath.Join("..", "..", "rules", "python"), filepath.Join(env.Dir, "elsewhere"))
	res, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n"), Rules: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res, 1, "app.py:1:9: "+py012)
	if err := os.RemoveAll(filepath.Join(env.Dir, ".june")); err != nil {
		t.Fatal(err)
	}
	res, _ = check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n"), Rules: "elsewhere"})
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
	if !strings.HasPrefix(out, "june: error: .june/rules/rule-tests/PY012-test.yml: an example of rule PY012 misbehaves (") ||
		!strings.HasSuffix(out, ". Fix: make the rule and its examples in rule-tests/PY012-test.yml agree\n") {
		t.Errorf("Out = %q", out)
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
		{name: "no list", out: "june: error: the touched lines are unknown. Fix: pass --changed-lines FILE, or - to read the list from stdin\n"},
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
			req := check.Request{Rules: tt.rules}
			if tt.list != nil {
				req.Changed = strings.NewReader(*tt.list)
			}
			res, err := check.Change(context.Background(), env, req)
			if err != nil {
				t.Fatal(err)
			}
			expect(t, res, 2, tt.out)
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
	expect(t, change(t, env, "\tapp.py:1\n"), 1, "\tapp.py:1:9: "+py012)
	expect(t, change(t, env, "app.py:1\n"), 0, "")
}

func TestFailureOutputHasNoAbsolutePaths(t *testing.T) {
	for _, failure := range []string{"rules directory", "rule file", "reader", "scan", "temporary directory"} {
		t.Run(failure, func(t *testing.T) {
			env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
			req := check.Request{Changed: strings.NewReader("app.py:1\n")}
			switch failure {
			case "rules directory":
				req.Rules = filepath.Join(env.Dir, "missing", "rules")
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
				write(t, filepath.Join(env.Dir, "app.py"), "# coding: latin-1\nx = '\xe9'\ndef f(a=[]): pass\n")
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
