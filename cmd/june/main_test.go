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
	"testing"
	"time"

	"github.com/vicolby/june/internal/check"
	"gopkg.in/yaml.v3"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got, want := out.String(), "june "+version+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestUnknownArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--bogus"}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

const py012 = "PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.\n"

// project builds a temp tree with PY012 at .june/rules and one Python file.
func project(t *testing.T) check.Env {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "..", "rules", "python")
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
	if code != 0 || out.String() != "june: verified 2 rules, 21 examples\n" {
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
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, project(t))
	want := "june: error: june was interrupted before it reached a verdict. Fix: run june again and let it finish\n"
	if code != 2 || errOut.String() != want || out.Len() != 0 {
		t.Errorf("exit = %d, stderr = %q, stdout = %q, want 2 and %q", code, errOut.String(), out.String(), want)
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
	if code != 0 || out.String() != "june: verified 1 rule, 19 examples\n" || errOut.Len() != 0 {
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
	want := "june: error: .june/rules/rule-tests/PY012-test.yml: rule PY012 valid example \"def f(a=[]): pass\" is reported by the rule. Fix: make the rule and its examples in rule-tests/PY012-test.yml agree\n"
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
	if code := run(context.Background(), []string{"--base", "main"}, strings.NewReader(""), &out, &errOut, env); code != 0 || out.String() != "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; base main (--base)\n" || errOut.Len() != 0 {
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

func TestShippedFundamentals(t *testing.T) {
	rulesDir, err := filepath.Abs(filepath.Join("..", "..", "rules", "python"))
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
	if code := run(context.Background(), []string{"verify", "--rules", rulesDir}, strings.NewReader(""), &out, &errOut, env); code != 0 || out.String() != "june: verified 5 rules, 135 examples\n" || errOut.Len() != 0 {
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
	if code := run(context.Background(), args, nil, &out, &errOut, env); code != 0 || out.String() != "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; base HEAD (staged diff)\n" || errOut.Len() != 0 {
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
