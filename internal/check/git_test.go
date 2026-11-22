package check_test

import (
	"context"
	"errors"
	"fmt"
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
	cmd := exec.CommandContext(context.Background(), "git", args...)
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

// refused checks a refusal: Exit 2, the exact message and no summary line.
func refused(t *testing.T, res check.Result, out string) {
	t.Helper()
	if res.Exit != 2 || string(res.Out) != out {
		t.Errorf("Exit = %d, Out =\n%s\nwant 2 and\n%s", res.Exit, res.Out, out)
	}
}

const old = "def f(a=[]): pass\n"

func TestGitScopeSparseFilesAreNotRenameSources(t *testing.T) {
	for _, source := range []string{"legacy.py", "legacy.txt", "tab\tlegacy.py"} {
		t.Run(source, func(t *testing.T) {
			env := repo(t, map[string]string{source: old, "keep/app.py": "x = 1\n"})
			git(t, env.Dir, "sparse-checkout", "init", "--no-cone")
			git(t, env.Dir, "sparse-checkout", "set", "--no-cone", "/.june/", "/keep/")
			if _, err := os.Lstat(filepath.Join(env.Dir, source)); !os.IsNotExist(err) {
				t.Fatalf("sparse file is not absent: %v", err)
			}
			copyDir(t, filepath.Join(env.Dir, ".june", "rules"), filepath.Join(env.Dir, "keep", ".june", "rules"))
			write(t, filepath.Join(env.Dir, "keep", "new.py"), old)
			sub := check.Env{Dir: filepath.Join(env.Dir, "keep")}
			for _, state := range []string{"untracked", "staged", "committed"} {
				if state == "staged" {
					git(t, env.Dir, "add", "keep/new.py")
				}
				if state == "committed" {
					git(t, env.Dir, "commit", "-q", "-m", "add new file")
				}
				indexPath := filepath.Join(env.Dir, ".git", "index")
				before, err := os.ReadFile(indexPath)
				if err != nil {
					t.Fatal(err)
				}
				expect(t, against(t, env, "main"), 1, "keep/new.py:1:9: "+py012)
				expect(t, against(t, sub, "main"), 1, "new.py:1:9: "+py012)
				after, err := os.ReadFile(indexPath)
				if err != nil || string(after) != string(before) {
					t.Fatalf("git scope changed the sparse index: %v", err)
				}
			}
		})
	}
}

func TestGitScopeAbsentSparseEditDoesNotHideCheckedOutFile(t *testing.T) {
	env := repo(t, map[string]string{"app.py": "x = 1\n", "keep/lib.py": "y = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 2\n")
	write(t, filepath.Join(env.Dir, "keep", "lib.py"), "def g(b={}): pass\n")
	commit(t, env)
	git(t, env.Dir, "sparse-checkout", "init", "--no-cone")
	git(t, env.Dir, "sparse-checkout", "set", "--no-cone", "/.june/", "/keep/")
	if _, err := os.Lstat(filepath.Join(env.Dir, "app.py")); !os.IsNotExist(err) {
		t.Fatalf("sparse file is not absent: %v", err)
	}
	indexPath := filepath.Join(env.Dir, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, against(t, env, "main"), 1, "keep/lib.py:1:9: "+py012)
	if _, err := os.Lstat(filepath.Join(env.Dir, "app.py")); !os.IsNotExist(err) {
		t.Fatalf("sparse file is no longer absent: %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("git scope changed the sparse index: %v", err)
	}
}

func TestGitScopeRenameSourceBecomesDirectory(t *testing.T) {
	for _, source := range []string{"old.txt", "old[1]*?.txt", "旧.txt"} {
		t.Run(source, func(t *testing.T) {
			body := "import os\nimport sys\nx = 1\ny = 2\n" + old
			env := repo(t, map[string]string{source: body})
			git(t, env.Dir, "mv", source, "new.py")
			asset := filepath.Join(env.Dir, source, "weights.bin")
			write(t, asset, "\x00"+strings.Repeat("a", 1<<20))
			write(t, filepath.Join(env.Dir, source, "child.py"), old)
			git(t, env.Dir, "add", "--", source)
			if err := os.Chmod(asset, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(asset, 0o644) })
			child := source + "/child.py:1:9: " + py012
			expect(t, against(t, env, "main"), 1, child)
			write(t, filepath.Join(env.Dir, "new.py"), body+"def g(b={}): pass\n")
			expect(t, against(t, env, "main"), 1, "new.py:6:9: "+py012+child)
		})
	}
}

func TestGitScopeDoesNotReadUnrelatedBinaryPatchContent(t *testing.T) {
	for _, extension := range []string{"bin", "py.bin"} {
		t.Run(extension, func(t *testing.T) {
			asset := "model." + extension
			body := "import os\nimport sys\nx = 1\ny = 2\n" + old
			env := repo(t, map[string]string{asset: "\x00" + strings.Repeat("a", 1<<20), "old.txt": body})
			write(t, filepath.Join(env.Dir, asset), "\x00"+strings.Repeat("b", 1<<20))
			if err := os.Rename(filepath.Join(env.Dir, "old.txt"), filepath.Join(env.Dir, "new.py")); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"unstaged", "staged", "committed"} {
				if state == "staged" {
					git(t, env.Dir, "add", "-A")
				}
				if state == "committed" {
					git(t, env.Dir, "commit", "-q", "-m", "move with binary update")
				}
				if err := os.Chmod(filepath.Join(env.Dir, asset), 0); err != nil {
					t.Fatal(err)
				}
				expect(t, against(t, env, "main"), 0, "")
				write(t, filepath.Join(env.Dir, "new.py"), body+"def g(b={}): pass\n")
				expect(t, against(t, env, "main"), 1, "new.py:6:9: "+py012)
				write(t, filepath.Join(env.Dir, "new.py"), body)
				if err := os.Chmod(filepath.Join(env.Dir, asset), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestGitScopeNormalizesIndexFlagsWithoutChangingCallerIndex(t *testing.T) {
	for _, flag := range []string{"core.ignorestat", "assume-unchanged", "skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
			if flag == "core.ignorestat" {
				git(t, env.Dir, "config", flag, "true")
				git(t, env.Dir, "update-index", "--really-refresh")
			} else {
				git(t, env.Dir, "update-index", "--"+flag, "--", "app.py")
			}
			indexPath := filepath.Join(env.Dir, ".git", "index")
			before, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			expect(t, against(t, env, "main"), 0, "")
			write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
			want := "app.py:2:9: " + py012
			expect(t, against(t, env, "main"), 1, want)
			write(t, filepath.Join(env.Dir, "unrelated.py"), "x = 1\n")
			expect(t, against(t, env, "main"), 1, want)
			if err := os.Remove(filepath.Join(env.Dir, "unrelated.py")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(env.Dir, "app.py")); err != nil {
				t.Fatal(err)
			}
			expect(t, against(t, env, "main"), 0, "")
			after, err := os.ReadFile(indexPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("git scope changed the caller's index: %v", err)
			}
		})
	}
}

func TestGitScopeReconstructedIndexReadsWorktree(t *testing.T) {
	env := repo(t, map[string]string{"empty.txt": "", "app.py": old + "x = 1\n"})
	git(t, env.Dir, "config", "core.ignorestat", "true")
	write(t, filepath.Join(env.Dir, "new.py"), old)
	expect(t, against(t, env, "main"), 1, "new.py:1:9: "+py012)
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	expect(t, against(t, env, "main"), 1, "app.py:2:9: "+py012+"new.py:1:9: "+py012)
}

func TestGitScopeBinaryTransitions(t *testing.T) {
	lateNUL := strings.Repeat("# padding\n", 900) + "\x00" + old
	for _, tc := range []struct {
		name   string
		before string
		after  string
		out    string
	}{
		{name: "binary to text", before: "\x00\x01\n", after: old, out: "app.py:1:9: " + py012},
		{name: "binary to empty", before: "\x00\x01\n", after: ""},
		{name: "text to binary", before: "x = 1\n", after: "\x00" + old},
		{name: "text to late binary", before: "x = 1\n", after: lateNUL},
		{name: "late binary to text", before: lateNUL, after: old, out: "app.py:1:9: " + py012},
		{name: "binary retains untouched violation", before: old + "x = '\x00'\n", after: old + "x = 'text'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := repo(t, map[string]string{"app.py": tc.before})
			write(t, filepath.Join(env.Dir, "app.py"), tc.after)
			exit := 0
			if tc.out != "" {
				exit = 1
			}
			expect(t, against(t, env, "main"), exit, tc.out)
			git(t, env.Dir, "add", "app.py")
			expect(t, against(t, env, "main"), exit, tc.out)
			git(t, env.Dir, "commit", "-q", "-m", "replace")
			expect(t, against(t, env, "main"), exit, tc.out)
		})
	}
}

func TestGitScopeNewBinaryWithLateNULIsSkipped(t *testing.T) {
	env := repo(t, nil)
	write(t, filepath.Join(env.Dir, "new.py"), strings.Repeat("# padding\n", 900)+"\x00"+old)
	expect(t, against(t, env, "main"), 0, "")
	git(t, env.Dir, "add", "new.py")
	expect(t, against(t, env, "main"), 0, "")
	git(t, env.Dir, "commit", "-q", "-m", "add binary")
	expect(t, against(t, env, "main"), 0, "")
}

func TestGitScopeBinaryToTextRenameJudgesOnlyEditedLines(t *testing.T) {
	body := strings.Repeat("x = 1\n", 10) + old
	env := repo(t, map[string]string{"old.py": body + "data = '\x00'\n"})
	if err := os.Rename(filepath.Join(env.Dir, "old.py"), filepath.Join(env.Dir, "new file.py")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(env.Dir, "new file.py"), body+"data = 'text'\ndef g(b={}): pass\n")
	want := "new file.py:13:9: " + py012
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "add", "-A")
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "commit", "-q", "-m", "rename and replace")
	expect(t, against(t, env, "main"), 1, want)
}

func TestGitScopeIgnoresContextSettings(t *testing.T) {
	for _, setting := range []string{"inter-hunk", "environment", "both"} {
		t.Run(setting, func(t *testing.T) {
			env := repo(t, map[string]string{"app.py": "x = 1\n" + old + "y = 1\n"})
			if setting != "environment" {
				git(t, env.Dir, "config", "diff.interHunkContext", "3")
			}
			if setting != "inter-hunk" {
				env.Vars = map[string]string{"GIT_DIFF_OPTS": "-u3"}
			}
			write(t, filepath.Join(env.Dir, "app.py"), "x = 2\n"+old+"y = 2\n")
			expect(t, against(t, env, "main"), 0, "")
		})
	}
}

func TestGitScopeIgnoresCallerGitEnvironment(t *testing.T) {
	for _, v := range []map[string]string{
		{"GIT_LITERAL_PATHSPECS": "1"},
		{"GIT_GLOB_PATHSPECS": "0"},
		{"GIT_NOGLOB_PATHSPECS": "1"},
		{"GIT_ICASE_PATHSPECS": "1"},
		{"GIT_EXTERNAL_DIFF": "false"},
	} {
		for name := range v {
			t.Run(name, func(t *testing.T) {
				env := repo(t, map[string]string{"app.py": "x = 1\n", "sub/App.py": "y = 1\n"})
				env.Vars = v
				write(t, filepath.Join(env.Dir, "app.py"), old)
				write(t, filepath.Join(env.Dir, "new.py"), old)
				expect(t, against(t, env, "main"), 1, "app.py:1:9: "+py012+"new.py:1:9: "+py012)
				commit(t, env)
				expect(t, against(t, env, "main"), 1, "app.py:1:9: "+py012+"new.py:1:9: "+py012)
			})
		}
	}
}

func TestGitScopePinsPathPrefixes(t *testing.T) {
	for _, setting := range [][2]string{{"diff.noprefix", "true"}, {"diff.mnemonicPrefix", "true"}, {"diff.dstPrefix", "custom/"}} {
		t.Run(setting[0], func(t *testing.T) {
			env := repo(t, map[string]string{"app.py": "x = 1\n", "b/app.py": "x = 1\n"})
			git(t, env.Dir, "config", setting[0], setting[1])
			write(t, filepath.Join(env.Dir, "b", "app.py"), old)
			expect(t, against(t, env, "main"), 1, "b/app.py:1:9: "+py012)
		})
	}
}

func TestGitScopeUnstagedRenames(t *testing.T) {
	for _, source := range []string{"old.py", "old.txt"} {
		t.Run(source, func(t *testing.T) {
			body := "import os\nimport sys\nx = 1\ny = 2\n" + old
			env := repo(t, map[string]string{source: body, "staged.py": "x = 1\n"})
			git(t, env.Dir, "config", "diff.renames", "false")
			if err := os.Mkdir(filepath.Join(env.Dir, "pkg"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(env.Dir, source), filepath.Join(env.Dir, "pkg", "new.py")); err != nil {
				t.Fatal(err)
			}
			indexPath := filepath.Join(env.Dir, ".git", "index")
			before, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			expect(t, against(t, env, "main"), 0, "")
			after, err := os.ReadFile(indexPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("git scope changed the real index: %v", err)
			}
			copyDir(t, filepath.Join(env.Dir, ".june", "rules"), filepath.Join(env.Dir, "pkg", ".june", "rules"))
			sub := check.Env{Dir: filepath.Join(env.Dir, "pkg")}
			expect(t, against(t, sub, "main"), 0, "")
			write(t, filepath.Join(env.Dir, "pkg", "new.py"), strings.Replace(body, "x = 1", "x = 1\ndef g(b={}): pass", 1))
			expect(t, against(t, env, "main"), 1, "pkg/new.py:4:9: "+py012)
			expect(t, against(t, sub, "main"), 1, "new.py:4:9: "+py012)
			write(t, filepath.Join(env.Dir, "staged.py"), old)
			git(t, env.Dir, "add", "staged.py")
			write(t, filepath.Join(env.Dir, "added.py"), old)
			git(t, env.Dir, "add", "added.py")
			expect(t, against(t, env, "main"), 1, "added.py:1:9: "+py012+"pkg/new.py:4:9: "+py012+"staged.py:1:9: "+py012)
		})
	}
}

func TestGitScopeHunkContentCannotChangePaths(t *testing.T) {
	for _, content := range []string{"++ x\n", "text = '''\n++ fake.py\n'''\n"} {
		t.Run(content, func(t *testing.T) {
			body := "x = 1\ny = 2\nz = 3\n"
			env := repo(t, map[string]string{"app.py": body, "other.py": "x = 1\n"})
			write(t, filepath.Join(env.Dir, "app.py"), content+body+old)
			write(t, filepath.Join(env.Dir, "other.py"), old)
			line := strings.Count(content+body, "\n") + 1
			expect(t, against(t, env, "main"), 1, fmt.Sprintf("app.py:%d:9: %sother.py:1:9: %s", line, py012, py012))
		})
	}
}

func TestGitScopeUnreadableUntrackedFileIsRefused(t *testing.T) {
	env := repo(t, nil)
	file := filepath.Join(env.Dir, "new.py")
	write(t, file, old)
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })
	res := against(t, env, "main")
	if res.Exit != 2 || !strings.Contains(string(res.Out), "Fix:") {
		t.Errorf("Exit = %d, Out = %q, want refusal", res.Exit, res.Out)
	}
	if strings.Contains(string(res.Out), env.Dir) {
		t.Errorf("Out has an absolute path: %q", res.Out)
	}
}

func TestGitScopeEmptyUntrackedFileTouchesNothing(t *testing.T) {
	env := repo(t, map[string]string{"legacy.py": old})
	write(t, filepath.Join(env.Dir, "empty.py"), "")
	expect(t, against(t, env, "main"), 0, "")
}

func TestGitScopeTemporaryIndexFailureIsRefused(t *testing.T) {
	env := repo(t, nil)
	write(t, filepath.Join(env.Dir, "new.py"), old)
	t.Setenv("TMPDIR", filepath.Join(env.Dir, "missing"))
	refused(t, against(t, env, "main"),
		"june: error: a temporary git index cannot be created. Fix: check temporary directory permissions and run june again\n")
}

func TestGitScopeSymlinksTouchNothing(t *testing.T) {
	for _, transition := range []bool{false, true} {
		t.Run(fmt.Sprint(transition), func(t *testing.T) {
			files := map[string]string{"legacy.py": old}
			if transition {
				files["alias.py"] = "x = 1\n"
			}
			env := repo(t, files)
			alias := filepath.Join(env.Dir, "alias.py")
			if transition {
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("legacy.py", alias); err != nil {
				t.Fatal(err)
			}
			expect(t, against(t, env, "main"), 0, "")
			git(t, env.Dir, "add", "alias.py")
			expect(t, against(t, env, "main"), 0, "")
			commit(t, env)
			expect(t, against(t, env, "main"), 0, "")
		})
	}
}

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
	want := "my app.py:1:9: " + py012 + `tab\tname.py:1:9: ` + py012 + "é.py:1:9: " + py012
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
	refused(t, against(t, env, "nope"),
		"june: error: the base \"nope\" is not a commit in this repository. Fix: fetch it, or pass an existing branch, tag or commit to --base\n")
	refused(t, against(t, env, "--output=x"),
		"june: error: the base \"--output=x\" is not a git ref. Fix: pass a branch, tag or commit to --base\n")

	git(t, env.Dir, "checkout", "-q", "--orphan", "island")
	git(t, env.Dir, "commit", "-q", "--allow-empty", "-m", "island")
	refused(t, against(t, env, "main"),
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

// infer runs a bare check: no base and no list.
func infer(t *testing.T, env check.Env) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, check.Request{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// clone makes a full clone of env's repository, checked out on a new branch that tracks nothing.
func clone(t *testing.T, env check.Env, args ...string) check.Env {
	t.Helper()
	git(t, env.Dir, "checkout", "-q", "main")
	dst := filepath.Join(t.TempDir(), "clone")
	git(t, env.Dir, append([]string{"clone", "-q", "--no-local"}, append(args, "file://"+env.Dir, dst)...)...)
	git(t, dst, "config", "user.name", "june")
	git(t, dst, "config", "user.email", "june@example.com")
	git(t, dst, "config", "commit.gpgsign", "false")
	return check.Env{Dir: dst}
}

func TestBareJuneInfersTheUpstreamBranch(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
	git(t, env.Dir, "branch", "-q", "--set-upstream-to=main", "topic")
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	res := infer(t, env)
	expect(t, res, 1, "app.py:2:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base main (upstream branch)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestBareJuneFindsRemoteUpstreamInAClone(t *testing.T) {
	origin := repo(t, map[string]string{"app.py": old})
	env := clone(t, origin)
	git(t, env.Dir, "checkout", "-q", "-b", "feature", "--track", "origin/main")
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	res := infer(t, env)
	expect(t, res, 1, "app.py:2:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base origin/main (upstream branch)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestBareJuneFallsBackToTheRemoteDefaultBranch(t *testing.T) {
	origin := repo(t, map[string]string{"app.py": old})
	env := clone(t, origin)
	git(t, env.Dir, "checkout", "-q", "-b", "feature", "--no-track", "origin/main")
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	res := infer(t, env)
	expect(t, res, 1, "app.py:2:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base origin/main (remote default branch)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestExplicitBaseNamesItsSource(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	if got, want := summaryOf(against(t, env, "main")), "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; base main (--base)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestUpstreamWinsOverTheRemoteDefaultBranch(t *testing.T) {
	origin := repo(t, map[string]string{"app.py": old})
	git(t, origin.Dir, "branch", "-q", "release", "main")
	env := clone(t, origin)
	git(t, env.Dir, "checkout", "-q", "-b", "feature", "--track", "origin/release")
	if got, want := summaryOf(infer(t, env)), "; base origin/release (upstream branch)"; !strings.HasSuffix(got, want) {
		t.Errorf("summary = %q, want suffix %q", got, want)
	}
}

func TestBareJuneWithoutAnyBaseRefusesInsteadOfCheckingEverything(t *testing.T) {
	// repo has a violation in a committed file: if the touched set were "every line", this would exit 1.
	env := repo(t, map[string]string{"app.py": old})
	refused(t, infer(t, env),
		"june: error: no base branch can be inferred because the current branch has no upstream and the repository has no remote default branch. Fix: pass --base REF, or --changed-lines FILE\n")
}

func TestBareJuneInAShallowCloneIsRefused(t *testing.T) {
	origin := repo(t, map[string]string{"app.py": old})
	write(t, filepath.Join(origin.Dir, "app.py"), old+"x = 1\n")
	git(t, origin.Dir, "checkout", "-q", "main")
	write(t, filepath.Join(origin.Dir, "main.py"), "y = 1\n")
	commit(t, origin)
	git(t, origin.Dir, "checkout", "-q", "topic")
	git(t, origin.Dir, "rebase", "-q", "--onto", "main~1", "main~1", "topic")
	env := clone(t, origin, "--depth=1", "--branch", "topic", "--no-single-branch")
	git(t, env.Dir, "checkout", "-q", "--no-track", "-b", "feature", "origin/topic")
	git(t, env.Dir, "remote", "set-head", "origin", "main")
	res := infer(t, env)
	if res.Exit != 2 || !strings.Contains(string(res.Out), "no merge base") || !strings.Contains(string(res.Out), "git fetch --unshallow") || strings.Contains(string(res.Out), "checked") {
		t.Errorf("Exit = %d, Out = %q, want a refusal naming git fetch --unshallow", res.Exit, res.Out)
	}
}

func TestBareJuneOutsideAGitRepositoryNamesTheFix(t *testing.T) {
	dir := t.TempDir()
	env := check.Env{Dir: dir, Vars: map[string]string{"GIT_CEILING_DIRECTORIES": filepath.Dir(dir)}}
	res := infer(t, env)
	if res.Exit != 2 || !strings.HasSuffix(string(res.Out), " is not inside a git work tree. Fix: run june inside the repository, or pass --changed-lines FILE\n") || strings.Contains(string(res.Out), "checked") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestBareJuneWithoutGitNamesTheFix(t *testing.T) {
	env := repo(t, map[string]string{"app.py": old})
	t.Setenv("PATH", t.TempDir())
	refused(t, infer(t, env),
		"june: error: git was not found, so the touched lines cannot be found. Fix: install git and put it on PATH, or pass --changed-lines FILE\n")
}

func TestSuppliedListNeedsNeitherGitNorABase(t *testing.T) {
	env := tree(t, map[string]string{"app.py": old})
	expect(t, change(t, env, "app.py:1\n"), 1, "app.py:1:9: "+py012)
	if got := summaryOf(change(t, env, "app.py:1\n")); strings.Contains(got, "base") {
		t.Errorf("summary = %q, want no base for a supplied list", got)
	}
}

func TestDubiousOwnershipNamesGitAndTheSafeDirectoryFix(t *testing.T) {
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	env.Vars = map[string]string{"GIT_TEST_ASSUME_DIFFERENT_OWNER": "1"}
	res := against(t, env, "main")
	out := string(res.Out)
	if res.Exit != 2 || !strings.HasPrefix(out, "june: error: git refuses to use this repository (fatal: detected dubious ownership") ||
		!strings.HasSuffix(out, "). Fix: git config --global --add safe.directory \"$PWD\"\n") || strings.Contains(out, "not inside a git work tree") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestOtherRevParseFailuresQuoteGitInsteadOfClaimingNoWorkTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := check.Env{Dir: dir, Vars: map[string]string{"GIT_CEILING_DIRECTORIES": filepath.Dir(dir)}}
	res := against(t, env, "main")
	out := string(res.Out)
	if res.Exit != 2 || !strings.HasPrefix(out, "june: error: git could not inspect the repository (fatal: invalid gitfile format") ||
		!strings.HasSuffix(out, "). Fix: run git rev-parse --is-inside-work-tree to see why\n") || strings.Contains(out, "not inside a git work tree") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestNonUTF8PythonFileIsNamedAndBlamesNeitherAstGrepNorTheScope(t *testing.T) {
	env := repo(t, map[string]string{"ok.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "pkg", "legacy.py"), "# -*- coding: latin-1 -*-\nx = '\xe9'\n")
	commit(t, env)
	res := against(t, env, "main")
	refused(t, res, "june: error: \"pkg/legacy.py\" is not UTF-8 text, so it cannot be scanned. Fix: save it as UTF-8 without NUL bytes and run june again\n")
}
