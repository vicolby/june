package check_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

// repo builds a git repository on branch main whose first commit holds the shipped rules and the given files.
func repo(t *testing.T, files map[string]string) check.Env {
	t.Helper()
	env := tree(t, files)
	git(t, env.Dir, "init", "-q", "-b", "main")
	git(t, env.Dir, "config", "user.name", "june")
	git(t, env.Dir, "config", "user.email", "june@example.com")
	git(t, env.Dir, "config", "commit.gpgsign", "false")
	git(t, env.Dir, "add", "-A")
	git(t, env.Dir, "commit", "-q", "-m", "base")
	git(t, env.Dir, "checkout", "-q", "-b", "topic")
	return env
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func commit(t *testing.T, env check.Env) {
	t.Helper()
	git(t, env.Dir, "add", "-A")
	git(t, env.Dir, "commit", "-q", "-m", "change")
}

func against(t *testing.T, env check.Env, base string) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, check.Request{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

const old = "def f(a=[]): pass\n"

func TestBaseJudgesCommittedEdits(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
	expect(t, against(t, env, "main"), 0, "")
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	expect(t, against(t, env, "main"), 1, "app.py:2:9: "+py012)
}

func TestBaseNeverFailsUntouchedLines(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old+"x = 2\n")
	expect(t, against(t, env, "main"), 0, "")
}

func TestBaseJudgesStagedAndUnstagedChanges(t *testing.T) {
	env := repo(t, map[string]string{"a.py": "x = 1\n", "b.py": "y = 1\n"})
	write(t, filepath.Join(env.Dir, "a.py"), "x = 1\n"+old)
	git(t, env.Dir, "add", "a.py")
	write(t, filepath.Join(env.Dir, "b.py"), "y = 1\n"+old)
	expect(t, against(t, env, "main"), 1, "a.py:2:9: "+py012+"b.py:2:9: "+py012)
}

func TestBaseUsesMergeBase(t *testing.T) {
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	git(t, env.Dir, "checkout", "-q", "main")
	write(t, filepath.Join(env.Dir, "other.py"), old)
	commit(t, env)
	git(t, env.Dir, "checkout", "-q", "topic")
	write(t, filepath.Join(env.Dir, "app.py"), "x = 2\n")
	commit(t, env)
	// main moved on with a violation the topic never touched.
	expect(t, against(t, env, "main"), 0, "")
}

func TestNewFileTouchesEveryLine(t *testing.T) {
	env := repo(t, map[string]string{"keep.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "new.py"), "x = 1\n"+old+"def g(b={}): pass")
	write(t, filepath.Join(env.Dir, "pkg", "deep.py"), old)
	want := "new.py:2:9: " + py012 + "new.py:3:9: " + py012 + "pkg/deep.py:1:9: " + py012
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "add", "-A")
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "commit", "-q", "-m", "add")
	expect(t, against(t, env, "main"), 1, want)
}

func TestUntrackedIgnoredFilesAreSkipped(t *testing.T) {
	env := repo(t, map[string]string{".gitignore": "ignored.py\n"})
	write(t, filepath.Join(env.Dir, "ignored.py"), old)
	expect(t, against(t, env, "main"), 0, "")
}

func TestDeletionsTouchNothing(t *testing.T) {
	env := repo(t, map[string]string{"gone.py": old, "app.py": old + "x = 1\n"})
	if err := os.Remove(filepath.Join(env.Dir, "gone.py")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n")
	expect(t, against(t, env, "main"), 0, "")
	commit(t, env)
	expect(t, against(t, env, "main"), 0, "")
}

func TestModeChangeTouchesNothing(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	if err := os.Chmod(filepath.Join(env.Dir, "app.py"), 0o755); err != nil {
		t.Fatal(err)
	}
	expect(t, against(t, env, "main"), 0, "")
	commit(t, env)
	expect(t, against(t, env, "main"), 0, "")
}

func TestPureRenameTouchesNothing(t *testing.T) {
	env := repo(t, map[string]string{"old.py": old})
	git(t, env.Dir, "-c", "diff.renames=false", "mv", "old.py", "new.py")
	expect(t, against(t, env, "main"), 0, "")
	commit(t, env)
	expect(t, against(t, env, "main"), 0, "")
}

func TestRenamedAndEditedFileIsJudgedByItsNewPath(t *testing.T) {
	body := "import os\nimport sys\nx = 1\ny = 2\n" + old
	env := repo(t, map[string]string{"old.py": body})
	if err := os.Mkdir(filepath.Join(env.Dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, env.Dir, "mv", "old.py", "pkg/new.py")
	write(t, filepath.Join(env.Dir, "pkg", "new.py"), strings.Replace(body, "x = 1", "x = 1\ndef g(b={}): pass", 1))
	want := "pkg/new.py:4:9: " + py012
	expect(t, against(t, env, "main"), 1, want)
	commit(t, env)
	expect(t, against(t, env, "main"), 1, want)
}

func TestBinaryAndNonPythonFilesAreSkipped(t *testing.T) {
	env := repo(t, map[string]string{"data.py": "\x00\x01", "notes.md": "a\n"})
	write(t, filepath.Join(env.Dir, "data.py"), "\x00"+old)
	write(t, filepath.Join(env.Dir, "notes.md"), old)
	write(t, filepath.Join(env.Dir, "new.md"), old)
	write(t, filepath.Join(env.Dir, "blob.py"), "\x00\x01"+old)
	expect(t, against(t, env, "main"), 0, "")
	commit(t, env)
	expect(t, against(t, env, "main"), 0, "")
}

func TestPathsWithSpacesAndUnicode(t *testing.T) {
	env := repo(t, map[string]string{"keep.py": "x = 1\n"})
	for _, name := range []string{"my app.py", "é.py", "tab\tname.py"} {
		write(t, filepath.Join(env.Dir, name), old)
	}
	want := "my app.py:1:9: " + py012 + "tab\tname.py:1:9: " + py012 + "é.py:1:9: " + py012
	expect(t, against(t, env, "main"), 1, want)
	commit(t, env)
	expect(t, against(t, env, "main"), 1, want)
}

func TestRunFromSubdirectoryUsesRelativePaths(t *testing.T) {
	env := repo(t, map[string]string{"keep.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "sub", "app.py"), old)
	write(t, filepath.Join(env.Dir, "top.py"), old)
	copyDir(t, filepath.Join("..", "..", "rules", "python"), filepath.Join(env.Dir, "sub", ".june", "rules"))
	sub := check.Env{Dir: filepath.Join(env.Dir, "sub")}
	expect(t, against(t, sub, "main"), 1, "app.py:1:9: "+py012)
	git(t, env.Dir, "add", "-A")
	expect(t, against(t, sub, "main"), 1, "app.py:1:9: "+py012)
}

func TestChangedWinsOverBase(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	res, err := check.Change(context.Background(), env, check.Request{Base: "main", Changed: strings.NewReader("app.py:1\n")})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res, 1, "app.py:1:9: "+py012)
}

func TestBaseRefusals(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	expect(t, against(t, env, "nope"), 2,
		"june: error: the base \"nope\" is not a commit in this repository. Fix: fetch it, or pass an existing branch, tag or commit to --base\n")
	expect(t, against(t, env, "--output=x"), 2,
		"june: error: the base \"--output=x\" is not a git ref. Fix: pass a branch, tag or commit to --base\n")

	git(t, env.Dir, "checkout", "-q", "--orphan", "island")
	git(t, env.Dir, "commit", "-q", "--allow-empty", "-m", "island")
	expect(t, against(t, env, "main"), 2,
		"june: error: HEAD and \"main\" have no merge base, so the touched lines are unknowable. Fix: fetch full history (git fetch --unshallow) or pass a base that shares history with HEAD\n")

	dir := t.TempDir()
	outside := check.Env{Dir: dir, Vars: map[string]string{"GIT_CEILING_DIRECTORIES": filepath.Dir(dir)}}
	res := against(t, outside, "main")
	if res.Exit != 2 || !strings.HasPrefix(string(res.Out), "june: error: the touched lines cannot be found because ") ||
		!strings.HasSuffix(string(res.Out), " is not inside a git work tree. Fix: run june inside the repository, or pass --changed-lines FILE\n") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
	if strings.Contains(string(res.Out), outside.Dir) {
		t.Errorf("Out has an absolute path: %q", res.Out)
	}
}

func TestShallowCloneWithoutMergeBaseIsRefused(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	write(t, filepath.Join(env.Dir, "app.py"), old+"x = 1\n")
	commit(t, env)
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, env.Dir, "clone", "-q", "--no-local", "--depth=1", "--branch", "topic", "file://"+env.Dir, clone)
	git(t, clone, "fetch", "-q", "--depth=1", "origin", "main:refs/remotes/origin/main")
	res := against(t, check.Env{Dir: clone}, "origin/main")
	if res.Exit != 2 || !strings.Contains(string(res.Out), "have no merge base") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestBaseCancelledContextIsAnError(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := check.Change(ctx, env, check.Request{Base: "main"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Change error = %v, want context.Canceled", err)
	}
}
