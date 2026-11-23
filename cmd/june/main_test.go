package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vicolby/june/internal/check"
	"gopkg.in/yaml.v3"
)

// astGrepVersion is the on-PATH `ast-grep --version` stdout. june --version and verify must print that line.
func astGrepVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "ast-grep", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got, want := out.String(), "june "+version+"\n"+astGrepVersion(t)+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestActionSmokeChecksEventOutcome(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID              string            `yaml:"id"`
				Uses            string            `yaml:"uses"`
				ContinueOnError bool              `yaml:"continue-on-error"`
				With            map[string]string `yaml:"with"`
				Env             map[string]string `yaml:"env"`
				Run             string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var assertion string
	var action bool
	for _, step := range workflow.Jobs["release-smoke"].Steps {
		if step.ID == "action-refusal" {
			action = step.Uses == "./" && step.ContinueOnError && step.With["base"] == "HEAD"
		}
		if step.Env["ACTION_OUTCOME"] == "${{ steps.action-refusal.outcome }}" {
			assertion = step.Run
		}
	}
	if !action || assertion == "" {
		t.Fatal("release smoke must run the action at HEAD and check its outcome")
	}
	for _, tc := range []struct {
		event, outcome string
		pass           bool
	}{
		{"pull_request", "failure", true},
		{"pull_request", "success", false},
		{"push", "success", true},
		{"push", "failure", false},
	} {
		cmd := exec.CommandContext(context.Background(), "bash", "-e", "-c", assertion)
		cmd.Env = []string{"GITHUB_EVENT_NAME=" + tc.event, "ACTION_OUTCOME=" + tc.outcome}
		if err := cmd.Run(); (err == nil) != tc.pass {
			t.Errorf("%s outcome %s: error = %v, want pass %v", tc.event, tc.outcome, err, tc.pass)
		}
	}
}

func TestHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"verify", "--help"}, {"verify", "-h"}, {"--legacy", "--help"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()}); code != 0 {
			t.Errorf("%v: exit = %d, want 0", args, code)
		}
		if errOut.Len() != 0 {
			t.Errorf("%v: stderr = %q, want empty", args, errOut.String())
		}
		for _, want := range []string{"usage:", "--base", "--changed-lines", "--exclude", "--legacy", "--ignore-whitespace", "--rules", "--version", "--help", "june verify", "exit codes:"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: stdout lacks %q: %q", args, want, out.String())
			}
		}
		if strings.Contains(out.String(), "--ignore-whitespace, -w") {
			t.Errorf("%v: help advertises -w as an alias: %q", args, out.String())
		}
	}
}

func TestUnknownArgs(t *testing.T) {
	for _, arg := range []string{"--bogus", "-w"} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{arg}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()})
		if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "flag provided but not defined: -"+strings.TrimLeft(arg, "-")) {
			t.Errorf("%s: exit = %d, stdout = %q, stderr = %q", arg, code, out.String(), errOut.String())
		}
	}
}

const py012 = "PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.\n"

// project builds a temp tree with PY012 at .june/rules and one Python file.
func project(t *testing.T) check.Env {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "rules", "python")
	for _, rel := range []string{"PY012.yml", filepath.Join("rule-tests", "PY012-test.yml")} {
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, ".june", "rules", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("x = 1\ndef f(a=[]):\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return check.Env{Dir: dir}
}

func TestChangedLinesFileFindingExitsOne(t *testing.T) {
	env := project(t)
	list := filepath.Join(t.TempDir(), "changed.txt")
	if err := os.WriteFile(list, []byte("app.py:2-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", list}, strings.NewReader(""), &out, &errOut, env)
	if code != 1 || out.String() != "app.py:2:9: "+py012+"june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 2 changed lines\n" || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestExcludeFlagKeepsPathsOutOfTheCheck(t *testing.T) {
	var out, errOut bytes.Buffer
	args := []string{"--exclude", "gen/**", "--exclude", "app.py", "--changed-lines", "-"}
	code := run(context.Background(), args, strings.NewReader("app.py:2\n"), &out, &errOut, project(t))
	if code != 0 || out.String() != "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; note: no changed Python files found - june reads .py files only, not .pyi or scripts without an extension - so if some were expected, check --base and the checkout; 1 file excluded\n" || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestExcludeFlagNeedsAValue(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--exclude"}, strings.NewReader(""), &out, &errOut, project(t)); code != 2 || !strings.Contains(errOut.String(), "Fix: usage is") {
		t.Errorf("exit = %d, stderr = %q, want a usage error", code, errOut.String())
	}
}

func TestChangedLinesStdinUntouchedExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:1\n"), &out, &errOut, project(t))
	if code != 0 || out.String() != "june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line\n" || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestRulesFlag(t *testing.T) {
	env := project(t)
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--rules", "nowhere", "--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, env)
	want := "june: error: nowhere: the rules directory does not exist. Fix: create it or point --rules at an existing rules directory\n"
	if code != 2 || out.String() != want {
		t.Errorf("exit = %d, stdout = %q, want 2 and %q", code, out.String(), want)
	}
}

func TestNoRulesSuppliedAsksForRules(t *testing.T) {
	want := "june: error: no rules to run: june ships no rules. Fix: supply rules with --rules DIR or put them in .june/rules\n"
	for _, args := range [][]string{{"--changed-lines", "-"}, {"verify"}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("x = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		code := run(context.Background(), args, strings.NewReader("app.py:1\n"), &out, &errOut, check.Env{Dir: dir})
		if code != 2 || out.String() != want {
			t.Errorf("%v: exit = %d, stdout = %q, want 2 and %q", args, code, out.String(), want)
		}
	}
}

func TestRulesFlagRepeats(t *testing.T) {
	env := project(t)
	shared := filepath.Join(env.Dir, "shared")
	if err := os.MkdirAll(filepath.Join(shared, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	rule := "id: LST01\nlanguage: python\nseverity: error\nmessage: No lists.\nrule:\n  kind: list\n"
	examples := "id: LST01\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n"
	for name, body := range map[string]string{"LST01.yml": rule, "rule-tests/LST01-test.yml": examples} {
		if err := os.WriteFile(filepath.Join(shared, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "app.py"), []byte("x = [1]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	args := []string{"--rules", ".june/rules", "--rules", "shared", "--changed-lines", "-"}
	code := run(context.Background(), args, strings.NewReader("app.py:1\n"), &out, &errOut, env)
	want := "app.py:1:5: LST01 No lists.\njune: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line\n"
	if code != 1 || out.String() != want {
		t.Errorf("exit = %d, stdout = %q, want 1 and %q", code, out.String(), want)
	}

	out.Reset()
	code = run(context.Background(), []string{"verify", "--rules", ".june/rules", "--rules", "shared"}, nil, &out, &errOut, env)
	if code != 0 || out.String() != "june: verified 2 rules, 21 examples with "+astGrepVersion(t)+"\n" {
		t.Errorf("verify exit = %d, stdout = %q", code, out.String())
	}

	out.Reset()
	code = run(context.Background(), []string{"--rules", "shared", "--rules", "shared/../shared", "--rules", ".june/rules", "--rules", "shared", "--changed-lines", "-"}, strings.NewReader("app.py:1\n"), &out, &errOut, env)
	if code != 1 || out.String() != want {
		t.Errorf("repeated dir: exit = %d, stdout = %q, want 1 and %q", code, out.String(), want)
	}
}

func TestBadListExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py\n"), &out, &errOut, project(t))
	if code != 2 || !strings.HasPrefix(out.String(), "june: error: changed-lines line 1") || !strings.Contains(out.String(), ". Fix: ") {
		t.Errorf("exit = %d, stdout = %q", code, out.String())
	}
}

func TestMissingListFileExitsTwo(t *testing.T) {
	for _, file := range []string{"absent.txt", filepath.Join(t.TempDir(), "absent.txt")} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"--changed-lines", file}, strings.NewReader(""), &out, &errOut, project(t))
		want := "june: error: the changed-lines file cannot be read. Fix: pass an existing file to --changed-lines\n"
		if code != 2 || errOut.String() != want || out.Len() != 0 {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
		}
	}
}

func TestInterruptedRunExplainsItself(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	want := "june: error: june was interrupted before it reached a verdict. Fix: run june again and let it finish\n"
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, project(t))
	if code != 2 || errOut.String() != want || out.Len() != 0 {
		t.Errorf("exit = %d, stderr = %q, stdout = %q, want 2 and %q", code, errOut.String(), out.String(), want)
	}

	for _, path := range []string{os.Getenv("PATH"), t.TempDir()} {
		t.Setenv("PATH", path)
		out.Reset()
		errOut.Reset()
		code = run(ctx, []string{"--version"}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()})
		if code != 2 || errOut.String() != want || out.Len() != 0 {
			t.Errorf("--version PATH=%q: exit = %d, stderr = %q, stdout = %q, want 2 and %q", path, code, errOut.String(), out.String(), want)
		}
	}
}

func TestInterruptedOpenStdinExplainsItself(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }() // io.Pipe closes always return nil.
	defer func() { _ = writer.Close() }()
	env := project(t)
	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--changed-lines", "-"}, reader, &out, &errOut, env)
	}()
	if _, err := io.WriteString(writer, "app.py:2\n"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case code := <-done:
		want := "june: error: june was interrupted before it reached a verdict. Fix: run june again and let it finish\n"
		if code != 2 || errOut.String() != want || out.Len() != 0 {
			t.Errorf("exit = %d, stderr = %q, stdout = %q, want 2 and %q", code, errOut.String(), out.String(), want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run stayed blocked on open stdin after cancellation")
	}
}

func TestMissingWorkingDirectoryExplainsItself(t *testing.T) {
	getwd := func() (string, error) { return "", errors.New("getwd: no such file or directory") }
	var out, errOut bytes.Buffer
	code := start(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, getwd)
	want := "june: error: the working directory cannot be determined (getwd: no such file or directory). Fix: run june from an existing directory\n"
	if code != 2 || out.Len() != 0 || errOut.String() != want {
		t.Errorf("exit = %d, stderr = %q, stdout = %q, want 2 and %q", code, errOut.String(), out.String(), want)
	}
}

func TestVerifyPrintsCounts(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"verify"}, strings.NewReader(""), &out, &errOut, project(t))
	if code != 0 || out.String() != "june: verified 1 rule, 19 examples with "+astGrepVersion(t)+"\n" || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestVerifyFaultExitsTwo(t *testing.T) {
	env := project(t)
	sidecar := filepath.Join(env.Dir, ".june", "rules", "rule-tests", "PY012-test.yml")
	if err := os.WriteFile(sidecar, []byte("id: PY012\nvalid:\n  - \"def f(a=[]): pass\"\ninvalid:\n  - \"def f(a=[]): pass\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"verify"}, strings.NewReader(""), &out, &errOut, env)
	want := "june: error: .june/rules/rule-tests/PY012-test.yml: rule PY012 valid example #1 \"def f(a=[]): pass\" is reported by the rule. Fix: make the rule and its examples in rule-tests/PY012-test.yml agree\n"
	if code != 2 || out.String() != want {
		t.Errorf("exit = %d, stdout = %q, want 2 and %q", code, out.String(), want)
	}
}

func TestVerifyArguments(t *testing.T) {
	for _, args := range [][]string{{"verify", "extra"}, {"verify", "--changed-lines", "-"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &errOut, project(t)); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), ". Fix: usage is") {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", args, code, out.String(), errOut.String())
		}
	}
}

func TestInvalidRuleGlobsNameTheirSource(t *testing.T) {
	for _, key := range []string{"files", "ignores"} {
		t.Run(key, func(t *testing.T) {
			env := project(t)
			shared := filepath.Join(env.Dir, "shared")
			if err := os.MkdirAll(filepath.Join(shared, "rule-tests"), 0o755); err != nil {
				t.Fatal(err)
			}
			rule := "id: BAD[01]\nlanguage: python\nseverity: off\nmessage: No lists.\nrule:\n  kind: list\n"
			path := filepath.Join(shared, "odd name.yaml")
			if err := os.WriteFile(path, []byte(rule+key+": ['src/**', '[unclosed']\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(shared, "rule-tests", "BAD[01]-test.yml"), []byte("id: BAD[01]\nvalid: ['x = 1']\ninvalid: ['x = [1]']\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, dirs := range [][]string{{".june/rules", "shared"}, {"shared", ".june/rules"}} {
				for _, command := range [][]string{{"verify"}, {"--changed-lines", "-"}} {
					args := append(append([]string{}, command...), "--rules", dirs[0], "--rules", dirs[1])
					var out, errOut bytes.Buffer
					code := run(context.Background(), args, strings.NewReader("app.py:1\n"), &out, &errOut, env)
					if code != 2 || !strings.HasPrefix(out.String(), "june: error: shared/odd name.yaml: ") || !strings.Contains(out.String(), "not a valid glob") || strings.Count(out.String(), "june: error:") != 1 || errOut.Len() != 0 {
						t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", args, code, out.String(), errOut.String())
					}
				}
			}
			if err := os.WriteFile(path, []byte(rule+key+": ['src/**']\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			if code := run(context.Background(), []string{"verify", "--rules", ".june/rules", "--rules", "shared"}, nil, &out, &errOut, env); code != 0 || !strings.HasPrefix(out.String(), "june: verified 2 rules, 21 examples with ") || errOut.Len() != 0 {
				t.Errorf("valid glob: exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestLegacyFlag(t *testing.T) {
	env := project(t)
	args := []string{"--changed-lines", "-"}
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, strings.NewReader("app.py:1\n"), &out, &errOut, env)
	if code != 0 || strings.Contains(out.String(), "legacy") {
		t.Errorf("default: exit = %d, stdout = %q, want 0 and no legacy output", code, out.String())
	}

	out.Reset()
	code = run(context.Background(), append([]string{"--legacy"}, args...), strings.NewReader("app.py:1\n"), &out, &errOut, env)
	want := "legacy: app.py:2:9: " + py012 + "june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; 1 legacy violation listed\n"
	if code != 0 || out.String() != want || errOut.Len() != 0 {
		t.Errorf("--legacy: exit = %d, stdout = %q, want 0 and %q", code, out.String(), want)
	}
}

func TestBaseFlagJudgesGitChanges(t *testing.T) {
	env := project(t)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "june"},
		{"config", "user.email", "june@example.com"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-q", "-m", "base"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = env.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--base", "main"}, strings.NewReader(""), &out, &errOut, env); code != 0 || out.String() != "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; note: no changed Python files found - june reads .py files only, not .pyi or scripts without an extension - so if some were expected, check --base and the checkout; base main (--base)\n" || errOut.Len() != 0 {
		t.Fatalf("unchanged tree: exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "new.py"), []byte("def g(b={}): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code := run(context.Background(), []string{"--base", "main"}, strings.NewReader(""), &out, &errOut, env)
	if code != 1 || out.String() != "new.py:1:9: "+py012+"june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base main (--base)\n" || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestBareJuneOutsideGitWithoutAListExitsTwo(t *testing.T) {
	dir := t.TempDir()
	env := check.Env{Dir: dir, Vars: map[string]string{"GIT_CEILING_DIRECTORIES": filepath.Dir(dir)}}
	var out, errOut bytes.Buffer
	code := run(context.Background(), nil, strings.NewReader(""), &out, &errOut, env)
	if code != 2 || !strings.Contains(out.String(), "Fix: run june inside the repository, or pass --changed-lines FILE") || strings.Contains(out.String(), "checked") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestFixtureFundamentals(t *testing.T) {
	rulesDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "rules", "python"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := "def f(a=[]):\n    pass\n" + // 1-2
		"try:\n    f()\nexcept:\n    pass\n" + // 3-6
		"if f() == None:\n    pass\n" + // 7-8
		"assert (f(), 'msg')\n" // 9
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	env := check.Env{Dir: dir}

	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"verify", "--rules", rulesDir}, strings.NewReader(""), &out, &errOut, env); code != 0 || out.String() != "june: verified 5 rules, 135 examples with "+astGrepVersion(t)+"\n" || errOut.Len() != 0 {
		t.Fatalf("verify exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code := run(context.Background(), []string{"--rules", rulesDir, "--changed-lines", "-"}, strings.NewReader("app.py:1-9\n"), &out, &errOut, env)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	for _, id := range []string{"app.py:1:", "PY012", "app.py:5:1: PY013", "app.py:6:5: PY015", "app.py:7:11: PY014", "app.py:9:8: PY016"} {
		if !strings.Contains(out.String(), id) {
			t.Errorf("stdout missing %q:\n%s", id, out.String())
		}
	}
}

func TestGitLabJobWritesCodeQualityReportBesideTheSameText(t *testing.T) {
	env := project(t)
	env.Vars = map[string]string{"GITLAB_CI": "true"}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:2-3\n"), &out, &errOut, env)
	if code != 1 || out.String() != "app.py:2:9: "+py012+"june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 2 changed lines\n" || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	report, err := os.ReadFile(filepath.Join(env.Dir, "gl-code-quality-report.json"))
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if !strings.Contains(string(report), `"check_name": "PY012"`) || !strings.Contains(string(report), `"severity": "major"`) {
		t.Errorf("report = %s", report)
	}
}

func TestDevShellWritesNoReport(t *testing.T) {
	env := project(t)
	var out, errOut bytes.Buffer
	run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:2-3\n"), &out, &errOut, env)
	if _, err := os.Stat(filepath.Join(env.Dir, "gl-code-quality-report.json")); !os.IsNotExist(err) {
		t.Errorf("a dev shell run wrote a report (stat err = %v)", err)
	}
}

func TestUnwritableReportExitsTwoAndKeepsTheVerdictText(t *testing.T) {
	env := project(t)
	env.Vars = map[string]string{"GITLAB_CI": "true"}
	if err := os.Mkdir(filepath.Join(env.Dir, "gl-code-quality-report.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:2-3\n"), &out, &errOut, env)
	if code != 2 || !strings.HasPrefix(errOut.String(), "june: error: the report gl-code-quality-report.json cannot be written (") || !strings.Contains(errOut.String(), ". Fix: ") {
		t.Errorf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "PY012") {
		t.Errorf("stdout = %q, want the findings still printed", out.String())
	}
}

// hookDefinition is the repo's published pre-commit hook, as pre-commit reads it.
type hookDefinition struct {
	ID            string   `yaml:"id"`
	Entry         string   `yaml:"entry"`
	Language      string   `yaml:"language"`
	Types         []string `yaml:"types"`
	PassFilenames *bool    `yaml:"pass_filenames"`
	Stages        []string `yaml:"stages"`
}

func publishedHook(t *testing.T) hookDefinition {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".pre-commit-hooks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var hooks []hookDefinition
	if err := yaml.Unmarshal(b, &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].ID != "june" {
		t.Fatalf("hooks = %+v, want exactly the june hook", hooks)
	}
	return hooks[0]
}

func TestPublishedHookRunsWithoutFilenamesAndOnlyForPython(t *testing.T) {
	h := publishedHook(t)
	if h.PassFilenames == nil || *h.PassFilenames {
		t.Errorf("pass_filenames = %v, want false: june finds its own scope", h.PassFilenames)
	}
	if len(h.Types) != 1 || h.Types[0] != "python" {
		t.Errorf("types = %v, want [python]", h.Types)
	}
	if h.Language != "system" || h.Entry != "june" {
		t.Errorf("language = %q, entry = %q, want system and june: a brew-installed binary on PATH", h.Language, h.Entry)
	}
	if got, want := strings.Join(h.Stages, ","), "pre-commit,pre-merge-commit,pre-push,manual"; got != want {
		t.Errorf("stages = %v, want pre-commit, pre-merge-commit, pre-push, and manual", h.Stages)
	}
}

// TestPublishedHookInThePreCommitEnvironment runs the hook's entry the way pre-commit does:
// no filenames, PRE_COMMIT=1, the working directory at the repository root, and the staged diff as the scope.
func TestPublishedHookInThePreCommitEnvironment(t *testing.T) {
	h := publishedHook(t)
	env := project(t)
	env.Vars = map[string]string{"PRE_COMMIT": "1"}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = env.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "june")
	git("config", "user.email", "june@example.com")
	git("config", "commit.gpgsign", "false")
	git("add", "-A")
	git("commit", "-q", "-m", "base") // app.py already holds a violation; it is legacy and must not fail
	var out, errOut bytes.Buffer
	args := strings.Fields(strings.TrimPrefix(h.Entry, "june"))
	if code := run(context.Background(), args, nil, &out, &errOut, env); code != 0 || out.String() != "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; note: no changed Python files found - june reads .py files only, not .pyi or scripts without an extension - so if some were expected, check --base and the checkout; base HEAD (staged diff)\n" || errOut.Len() != 0 {
		t.Fatalf("nothing staged: exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "new.py"), []byte("def g(b={}): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "new.py")
	out.Reset()
	code := run(context.Background(), args, nil, &out, &errOut, env)
	if code != 1 || out.String() != "new.py:1:9: "+py012+"june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base HEAD (staged diff)\n" || errOut.Len() != 0 {
		t.Errorf("staged violation: exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

// TestTerminatedRunCleansUpAndExplainsItself runs the built binary, stops it with SIGTERM
// (CI cancel, docker stop, timeout) while ast-grep scans, and expects the SIGINT outcome:
// the explanation, exit 2 and no june-scan-* directory left in TMPDIR.
func TestTerminatedRunCleansUpAndExplainsItself(t *testing.T) {
	realGrep, err := exec.LookPath("ast-grep")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "june")
	if out, err := exec.CommandContext(context.Background(), "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	tools := t.TempDir()
	scanning := filepath.Join(tools, "scanning")
	wrapper := "#!/bin/sh\nif [ \"$1\" = scan ]; then : > " + scanning + "; exec sleep 60; fi\nexec " + realGrep + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tools, "ast-grep"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	env := project(t)
	list := filepath.Join(env.Dir, "changed.txt")
	if err := os.WriteFile(list, []byte("app.py:2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	cmd := exec.CommandContext(context.Background(), bin, "--changed-lines", list)
	cmd.Dir = env.Dir
	cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+scratch)
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	const poll = 20 * time.Millisecond
	for waited := time.Duration(0); ; waited += poll {
		if _, err := os.Stat(scanning); err == nil {
			break
		}
		if waited >= 30*time.Second {
			_ = cmd.Process.Kill()
			t.Fatalf("ast-grep never started scanning; stderr = %q", errOut.String())
		}
		time.Sleep(poll)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Errorf("Wait = %v, want exit 2 like SIGINT", err)
	}
	want := "june: error: june was interrupted before it reached a verdict. Fix: run june again and let it finish\n"
	if errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	left, err := filepath.Glob(filepath.Join(scratch, "june-*"))
	if err != nil || len(left) > 0 {
		t.Errorf("temporary directories left behind: %v, %v", left, err)
	}
}

func runFormat(t *testing.T, env check.Env, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append(args, "--changed-lines", "-")
	code := run(context.Background(), args, strings.NewReader("app.py:2-3\n"), &out, &errOut, env)
	return code, out.String(), errOut.String()
}

func TestFormatFlagChoosesTheFormatAnywhere(t *testing.T) {
	for _, tc := range []struct {
		format, want string
	}{
		{"json", `"rule": "PY012"`},
		{"sarif", `"ruleId": "PY012"`},
		{"github", "::error file=app.py,line=2,col=9,title=PY012::"},
		{"text", "app.py:2:9: PY012"},
	} {
		code, out, errOut := runFormat(t, project(t), "--format", tc.format)
		if code != 1 || !strings.Contains(out, tc.want) || errOut != "" {
			t.Errorf("--format %s: exit = %d, stdout = %q, stderr = %q", tc.format, code, out, errOut)
		}
	}
}

func TestFormatFlagOverridesTheInferredFormat(t *testing.T) {
	env := project(t)
	env.Vars = map[string]string{"GITLAB_CI": "true"}
	code, out, _ := runFormat(t, env, "--format", "text")
	if code != 1 || strings.Contains(out, "::") {
		t.Errorf("exit = %d, stdout = %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "gl-code-quality-report.json")); !os.IsNotExist(err) {
		t.Errorf("--format text still wrote the GitLab report (stat err = %v)", err)
	}
}

func TestFormatGitLabWritesTheReportOutsideGitLabCI(t *testing.T) {
	env := project(t)
	code, out, _ := runFormat(t, env, "--format", "gitlab")
	if code != 1 || !strings.HasPrefix(out, "app.py:2:9: PY012") {
		t.Errorf("exit = %d, stdout = %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "gl-code-quality-report.json")); err != nil {
		t.Errorf("report not written: %v", err)
	}
}

func TestUnknownFormatIsAUsageError(t *testing.T) {
	code, out, errOut := runFormat(t, project(t), "--format", "xml")
	if code != 2 || out != "" || !strings.HasPrefix(errOut, `june: error: unknown format "xml". Fix: usage is `) {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

func TestSARIFRefusalIsStillAValidLog(t *testing.T) {
	env := project(t)
	code, out, _ := runFormat(t, env, "--format", "sarif", "--rules", "missing")
	if code != 2 || !strings.Contains(out, `"executionSuccessful": false`) || !strings.Contains(out, `"results": []`) {
		t.Errorf("exit = %d, stdout = %q", code, out)
	}
}

func TestStepSummaryIsAppendedWhenTheCIsetsIt(t *testing.T) {
	env := project(t)
	summary := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(summary, []byte("earlier step\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.Vars = map[string]string{"GITHUB_STEP_SUMMARY": summary}
	code, out, errOut := runFormat(t, env)
	if code != 1 || !strings.HasPrefix(out, "app.py:2:9: PY012") || errOut != "" {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
	got, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "earlier step\n## june\n") || !strings.Contains(string(got), "| error | `app.py:2:9` | PY012 |") {
		t.Errorf("summary = %q", got)
	}
	// A second june step adds to the file instead of replacing it.
	runFormat(t, env)
	if again, _ := os.ReadFile(summary); strings.Count(string(again), "## june") != 2 {
		t.Errorf("second run replaced the summary: %q", again)
	}
}

func TestStepSummaryWrittenForARefusalAndEveryFormat(t *testing.T) {
	env := project(t)
	summary := filepath.Join(t.TempDir(), "summary.md")
	env.Vars = map[string]string{"GITHUB_STEP_SUMMARY": summary}
	runFormat(t, env, "--format", "sarif", "--rules", "missing")
	got, _ := os.ReadFile(summary)
	if !strings.Contains(string(got), "| error |  |  | ") {
		t.Errorf("summary = %q", got)
	}
}

func TestUnwritableStepSummaryWarnsButKeepsTheVerdict(t *testing.T) {
	env := project(t)
	env.Vars = map[string]string{"GITHUB_STEP_SUMMARY": filepath.Join(t.TempDir(), "missing", "summary.md")}
	code, out, errOut := runFormat(t, env)
	if code != 1 || !strings.Contains(out, "PY012") || !strings.HasPrefix(errOut, "june: warning: the job summary ") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

func TestVerifyIgnoresStepSummary(t *testing.T) {
	env := project(t)
	summary := filepath.Join(t.TempDir(), "summary.md")
	env.Vars = map[string]string{"GITHUB_STEP_SUMMARY": summary}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"verify"}, strings.NewReader(""), &out, &errOut, env); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if _, err := os.Stat(summary); !os.IsNotExist(err) {
		t.Errorf("verify wrote a job summary (stat err = %v)", err)
	}
}

func TestIgnoreWhitespaceFlagRefusesChangedLines(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--ignore-whitespace", "--changed-lines", "-"}, strings.NewReader("app.py:1\n"), &out, &errOut, project(t))
	if code != 2 || !strings.Contains(out.String()+errOut.String(), "--ignore-whitespace has no effect on --changed-lines") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestInternalErrorNamesWhereToReportAndTheVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	// An empty working directory is an internal error from check, not a usage problem.
	if code := run(context.Background(), nil, strings.NewReader(""), &out, &errOut, check.Env{}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	want := "Fix: report this as a june bug at https://github.com/vicolby/june/issues with june " + version
	if !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", errOut.String(), want)
	}
}

func runArgs(t *testing.T, env check.Env, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(""), &out, &errOut, env)
	return code, out.String(), errOut.String()
}

func TestNewScaffoldsARuleThatVerifies(t *testing.T) {
	env := check.Env{Dir: t.TempDir()}
	for _, args := range [][]string{{"new", "MY001"}, {"new", "--rules", "other", "MY002"}} {
		if code, out, errOut := runArgs(t, env, args...); code != 0 || !strings.HasPrefix(out, "created ") || errOut != "" {
			t.Fatalf("%v: exit = %d, stdout = %q, stderr = %q", args, code, out, errOut)
		}
	}
	for _, args := range [][]string{{"verify"}, {"verify", "--rules", "other", "--rule", "MY002", "--format", "json"}} {
		if code, out, errOut := runArgs(t, env, args...); code != 0 || !strings.Contains(out, "verified") || errOut != "" {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", args, code, out, errOut)
		}
	}
}

func TestNewSuggestedCommandVerifiesTheChosenDirectory(t *testing.T) {
	for _, dir := range []string{"", "mine", "author's rules $HOME; $(printf wrong) `printf wrong`"} {
		t.Run(dir, func(t *testing.T) {
			env := check.Env{Dir: t.TempDir()}
			args := []string{"new", "null"}
			if dir != "" {
				args = []string{"new", "--rules", dir, "null"}
			}
			code, out, errOut := runArgs(t, env, args...)
			if code != 0 || errOut != "" {
				t.Fatalf("new = %d, %q, %q", code, out, errOut)
			}
			_, command, ok := strings.Cut(out, "then run ")
			if !ok {
				t.Fatalf("no suggested command: %q", out)
			}
			cmd := exec.Command("sh", "-c", "june() { printf '%s\\000' \"$@\"; }; "+command)
			cmd.Dir = env.Dir
			parsed, err := cmd.Output()
			if err != nil {
				t.Fatalf("parse suggested command: %v", err)
			}
			verifyArgs := strings.Split(strings.TrimSuffix(string(parsed), "\x00"), "\x00")
			if code, out, errOut := runArgs(t, env, verifyArgs...); code != 0 || errOut != "" || !strings.Contains(out, "verified 1 rule, 2 examples") {
				t.Errorf("suggested command = %d, %q, %q", code, out, errOut)
			}
		})
	}
}

func TestNewCleansUpAfterWriteFailure(t *testing.T) {
	const helperDir = "JUNE_TEST_FAILED_WRITE_DIR"
	if dir := os.Getenv(helperDir); dir != "" {
		if code, out, errOut := runArgs(t, check.Env{Dir: dir}, "new", "MY001"); code != 2 || !strings.Contains(out, "cannot be written") || errOut != "" {
			t.Fatalf("new with a file-size limit = %d, %q, %q", code, out, errOut)
		}
		return
	}
	env := check.Env{Dir: t.TempDir()}
	cmd := exec.Command("sh", "-c", `ulimit -f 0 && exec "$1" -test.run '^TestNewCleansUpAfterWriteFailure$'`, "sh", os.Args[0])
	cmd.Env = append(os.Environ(), helperDir+"="+env.Dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("limited child process: %v\n%s", err, out)
	}
	for _, rel := range []string{"MY001.yml", "rule-tests/MY001-test.yml"} {
		if _, err := os.Stat(filepath.Join(env.Dir, ".june", "rules", rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("failed new left %s: %v", rel, err)
		}
	}
	if code, out, errOut := runArgs(t, env, "new", "MY001"); code != 0 || errOut != "" {
		t.Fatalf("retry = %d, %q, %q", code, out, errOut)
	}
}

func TestNewRollsBackWhenExamplesCannotBeCreated(t *testing.T) {
	env := check.Env{Dir: t.TempDir()}
	blocked := filepath.Join(env.Dir, "rule-tests")
	if err := os.WriteFile(blocked, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runArgs(t, env, "new", "--rules", ".", "MY001"); code != 2 || !strings.Contains(out, "cannot be written") || errOut != "" {
		t.Fatalf("new with blocked examples = %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "MY001.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed new left the rule file: %v", err)
	}
	if b, err := os.ReadFile(blocked); err != nil || string(b) != "keep" {
		t.Fatalf("blocking file = %q, %v, want unchanged", b, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runArgs(t, env, "new", "--rules", ".", "MY001"); code != 0 || errOut != "" {
		t.Fatalf("retry = %d, %q, %q", code, out, errOut)
	}
}

func TestNewArguments(t *testing.T) {
	env := check.Env{Dir: t.TempDir()}
	for _, args := range [][]string{{"new"}, {"new", "A", "B"}, {"new", "--format", "json", "A"}, {"new", "--bogus"}, {"new", "MY001", "--rules", "other"}} {
		code, out, errOut := runArgs(t, env, args...)
		if code != 2 || out != "" || !strings.Contains(errOut, ". Fix: usage is") || !strings.Contains(errOut, "june new [--rules DIR] RULE") {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q, want a usage error", args, code, out, errOut)
		}
	}
	if entries, _ := os.ReadDir(env.Dir); len(entries) != 0 {
		t.Errorf("a refused new wrote %d entries", len(entries))
	}
}

func TestVerifyOneRuleAsJSON(t *testing.T) {
	env := project(t)
	code, out, errOut := runArgs(t, env, "verify", "--rule", "PY012", "--format", "json")
	if code != 0 || errOut != "" || !strings.Contains(out, `"diagnostics": []`) || !strings.Contains(out, "verified 1 rule, 19 examples") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
	code, out, errOut = runArgs(t, env, "verify", "--rule", "NOPE")
	if code != 2 || !strings.Contains(out, "no rule has the id NOPE (rules found: PY012)") || errOut != "" {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

func TestVerifyUnknownFormatIsUsageError(t *testing.T) {
	code, out, errOut := runArgs(t, project(t), "verify", "--format", "xml")
	if code != 2 || out != "" || !strings.Contains(errOut, `unknown format "xml"`) {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

func TestHelpListsTheAuthoringCommands(t *testing.T) {
	_, out, _ := runArgs(t, project(t), "--help")
	for _, want := range []string{"june verify [--rules DIR]... [--rule ID] [--format FORMAT]", "june new [--rules DIR] RULE", "--rule ID"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}
