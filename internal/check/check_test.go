package check_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	expect(t, change(t, env, "app.py:3\n"), 1, "app.py:3:10: "+py012)
}

func TestRangeTouchesEveryLineInIt(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = 1\ndef f(a=[]):\n    pass\n"})
	expect(t, change(t, env, "app.py:1-2\n"), 1, "app.py:2:7: "+py012)
	expect(t, change(t, env, "app.py:2-2\n"), 1, "app.py:2:7: "+py012)
}

func TestMatchOnUntouchedLineNeverFails(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]):\n    pass\n\nx = 1\n"})
	expect(t, change(t, env, "app.py:3-4\n"), 0, "")
}

func TestMatchIsJudgedOnItsFirstLineOnly(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(\n    a=[\n        1,\n        2,\n    ],\n):\n    pass\n"})
	// The mutable default starts on line 2. Touching its later lines is not enough.
	expect(t, change(t, env, "app.py:3-5\n"), 0, "")
	expect(t, change(t, env, "app.py:2\n"), 1, "app.py:2:5: "+py012)
}

func TestOutputIsSortedAndPortable(t *testing.T) {
	env := tree(t, map[string]string{
		"z.py":     "def f(a=[]): pass\n",
		"pkg/a.py": "x = 1\ndef g(b={}): pass\ndef h(c=[]): pass\n",
	})
	res := change(t, env, "z.py:1\npkg/a.py:3\npkg/a.py:2\n")
	want := "pkg/a.py:2:7: " + py012 + "pkg/a.py:3:7: " + py012 + "z.py:1:7: " + py012
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
	expect(t, change(t, env, "./pkg/../pkg/app.py:1\n"), 1, "pkg/app.py:1:7: "+py012)
}

func TestRulesFlagOverridesDefault(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "def f(a=[]): pass\n"})
	copyDir(t, filepath.Join("..", "..", "rules", "python"), filepath.Join(env.Dir, "elsewhere"))
	res, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n"), Rules: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res, 1, "app.py:1:7: "+py012)
	if err := os.RemoveAll(filepath.Join(env.Dir, ".june")); err != nil {
		t.Fatal(err)
	}
	res, _ = check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n"), Rules: "elsewhere"})
	expect(t, res, 1, "app.py:1:7: "+py012)
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
		{name: "not an entry", list: ptr("app.py\n"), out: "june: error: changed-lines line 1, \"app.py\", is not valid: expected path:N or path:N-M. Fix: " + listFix + "\n"},
		{name: "line zero", list: ptr("app.py:0\n"), out: "june: error: changed-lines line 1, \"app.py:0\", is not valid: \"0\" is not a line number from 1. Fix: " + listFix + "\n"},
		{name: "reversed range", list: ptr("\napp.py:9-3\n"), out: "june: error: changed-lines line 2, \"app.py:9-3\", is not valid: \"3\" is not a line number from 9. Fix: " + listFix + "\n"},
		{name: "words", list: ptr("app.py:x\n"), out: "june: error: changed-lines line 1, \"app.py:x\", is not valid: \"x\" is not a line number from 1. Fix: " + listFix + "\n"},
		{name: "absolute path", list: ptr("/etc/app.py:1\n"), out: "june: error: changed-lines line 1, \"/etc/app.py:1\", is not valid: the path \"/etc/app.py\" is not inside the repository. Fix: " + listFix + "\n"},
		{name: "escaping path", list: ptr("../app.py:1\n"), out: "june: error: changed-lines line 1, \"../app.py:1\", is not valid: the path \"../app.py\" is not inside the repository. Fix: " + listFix + "\n"},
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
	if _, err := check.Change(ctx, env, check.Request{Changed: strings.NewReader("app.py:1\n")}); err == nil {
		t.Error("Change with a cancelled context returned no error")
	}
}
