package check_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

// hook is the environment pre-commit gives a hook: PRE_COMMIT=1, and with --from-ref/--to-ref the two ref variables.
func hook(env check.Env, vars map[string]string) check.Env {
	merged := map[string]string{"PRE_COMMIT": "1"}
	for k, v := range vars {
		merged[k] = v
	}
	env.Vars = merged
	return env
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

func TestStagedChangeIsJudgedAgainstHEAD(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"}) // topic has no upstream, so only the staged diff can give a base
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	git(t, env.Dir, "add", "app.py")
	res := infer(t, hook(env, nil))
	expect(t, res, 1, "app.py:2:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base HEAD (staged diff)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestStagedDiffIgnoresEarlierCommitsOnTheBranch(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	git(t, env.Dir, "branch", "-q", "--set-upstream-to=main", "topic")
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	commit(t, env) // committed after the upstream, so a local run would judge it
	write(t, filepath.Join(env.Dir, "other.py"), "y = 1\n")
	git(t, env.Dir, "add", "other.py")
	expect(t, infer(t, hook(env, nil)), 0, "")
	expect(t, infer(t, env), 1, "app.py:2:9: "+py012)
}

func TestStagedDiffIgnoresUnstagedAndUntrackedFiles(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n", "staged.py": "y = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	write(t, filepath.Join(env.Dir, "new.py"), old)
	write(t, filepath.Join(env.Dir, "staged.py"), "y = 2\n")
	git(t, env.Dir, "add", "staged.py")
	res := infer(t, hook(env, nil))
	expect(t, res, 0, "")
	if got, want := summaryOf(res), "june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base HEAD (staged diff)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestStagedNewFileTouchesEveryLine(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"keep.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "new.py"), "x = 1\n"+old)
	git(t, env.Dir, "add", "new.py")
	expect(t, infer(t, hook(env, nil)), 1, "new.py:2:9: "+py012)
}

func TestStagedNonPythonAndDeletionsTouchNothing(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"gone.py": old, "notes.txt": "a\n"})
	git(t, env.Dir, "rm", "-q", "gone.py")
	write(t, filepath.Join(env.Dir, "notes.txt"), old)
	git(t, env.Dir, "add", "notes.txt")
	res := infer(t, hook(env, nil))
	expect(t, res, 0, "")
	if got, want := summaryOf(res), "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines"+noPython+"; base HEAD (staged diff)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestStagedFirstCommitIsJudgedAgainstAnEmptyTree(t *testing.T) {
	t.Parallel()
	env := tree(t, map[string]string{"app.py": "x = 1\n" + old})
	git(t, env.Dir, "init", "-q", "-b", "main")
	git(t, env.Dir, "add", "-A")
	res := infer(t, hook(env, nil))
	expect(t, res, 1, "app.py:2:9: "+py012)
	id := emptyTreeID(t, env.Dir)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 2 changed lines; base "+id+" (staged diff)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestStagedFirstCommitOnSHA256UsesThatEmptyTree(t *testing.T) {
	t.Parallel()
	env := tree(t, map[string]string{"app.py": "x = 1\n" + old})
	cmd := exec.CommandContext(context.Background(), "git", "init", "-q", "--object-format=sha256", "-b", "main")
	cmd.Dir = env.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("sha256 repositories are unavailable: %v\n%s", err, out)
	}
	git(t, env.Dir, "add", "-A")
	res := infer(t, hook(env, nil))
	expect(t, res, 1, "app.py:2:9: "+py012)
	id := emptyTreeID(t, env.Dir)
	if strings.HasPrefix(id, "4b825dc642cb6eb9a060e54bf8d69288fbee4904") {
		t.Fatal("sha256 repository reported the sha-1 empty tree")
	}
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 2 changed lines; base "+id+" (staged diff)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func emptyTreeID(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "hash-object", "-t", "tree", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestStagedDiffFollowsThePartialCommitIndex(t *testing.T) {
	t.Parallel()
	// pre-commit points GIT_INDEX_FILE at the index the commit will use, which differs from .git/index for git commit <paths>.
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	index := filepath.Join(t.TempDir(), "index")
	git(t, env.Dir, "read-tree", "--index-output="+index, "HEAD")
	staged := hook(env, map[string]string{"GIT_INDEX_FILE": index})
	expect(t, infer(t, staged), 0, "")
	cmd := exec.CommandContext(context.Background(), "git", "add", "app.py")
	cmd.Dir = env.Dir
	cmd.Env = append(cmd.Environ(), "GIT_INDEX_FILE="+index)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	expect(t, infer(t, staged), 1, "app.py:2:9: "+py012)
	write(t, filepath.Join(env.Dir, "app.py"), "x = 2\n") // the worktree no longer has the violation; the index still does
	expect(t, infer(t, staged), 1, "app.py:2:9: "+py012)
}

func TestStagedViolationStaysWhenTheWorktreeDropsIt(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old)
	git(t, env.Dir, "add", "app.py")
	write(t, filepath.Join(env.Dir, "app.py"), "x = 2\n")
	expect(t, infer(t, hook(env, nil)), 1, "app.py:1:9: "+py012)
}

func TestStagedFileWithASpaceUsesTheIndexBlob(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "new file.py"), old)
	git(t, env.Dir, "add", "new file.py")
	write(t, filepath.Join(env.Dir, "new file.py"), "x = 1\n")
	expect(t, infer(t, hook(env, nil)), 1, "new file.py:1:9: "+py012)
}

func TestStagedNULFilterUsesTheIndexBlob(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old+"\x00")
	git(t, env.Dir, "add", "app.py")
	write(t, filepath.Join(env.Dir, "app.py"), old) // the worktree has the violation; the index blob does not
	res := infer(t, hook(env, nil))
	expect(t, res, 0, skippedText("app.py", "it contains NUL bytes"))
	wantSkippedSummary(t, res, 1)
}

func TestStagedSymlinkIsNotScannedAsPython(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	path := filepath.Join(env.Dir, "app.py")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strings.TrimSuffix(old, "\n"), path); err != nil {
		t.Fatal(err)
	}
	git(t, env.Dir, "add", "app.py")
	expect(t, infer(t, hook(env, nil)), 0, "")
}

func TestPreCommitRefsJudgeTheRefRange(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	write(t, filepath.Join(env.Dir, "staged.py"), old)
	git(t, env.Dir, "add", "staged.py") // staged files are not part of the pushed range
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	res := infer(t, refs)
	expect(t, res, 1, "app.py:2:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base main (pre-commit refs)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestPreCommitRefsAcceptACommitHashFrom(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
	from := revParse(t, env.Dir, "main")
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": from, "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	expect(t, infer(t, refs), 1, "app.py:2:9: "+py012)
}

func TestPreCommitRefsNeedBothVariables(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	git(t, env.Dir, "add", "app.py")
	for name, vars := range map[string]map[string]string{
		"from only": {"PRE_COMMIT_FROM_REF": "main"},
		"to only":   {"PRE_COMMIT_TO_REF": "HEAD"},
		"empty":     {"PRE_COMMIT_FROM_REF": "", "PRE_COMMIT_TO_REF": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got, want := summaryOf(infer(t, hook(env, vars))), "; base HEAD (staged diff)"; !strings.HasSuffix(got, want) {
				t.Errorf("summary = %q, want suffix %q", got, want)
			}
		})
	}
}

func TestPreCommitRefsPassForACleanToRefWhileCheckedOutElsewhere(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	commit(t, env) // topic, which is checked out, contains the violation; main does not
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "main")})
	expect(t, infer(t, refs), 0, "")
}

func TestPreCommitRefsReportTheToRefWhileCheckedOutElsewhere(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	commit(t, env)
	topic := revParse(t, env.Dir, "HEAD")
	git(t, env.Dir, "checkout", "-q", "main")
	write(t, filepath.Join(env.Dir, "app.py"), old) // staged on the checkout, not in the commits being pushed
	git(t, env.Dir, "add", "app.py")
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": topic})
	expect(t, infer(t, refs), 1, "app.py:2:9: "+py012)
}

func TestPreCommitRefsScanTheCommittedLineWhenTheIndexShiftsIt(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	commit(t, env)
	write(t, filepath.Join(env.Dir, "app.py"), "y = 1\n"+"x = 1\n"+old)
	git(t, env.Dir, "add", "app.py")
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	expect(t, infer(t, refs), 1, "app.py:2:9: "+py012)
}

func TestPreCommitRefsIgnoreAnIndexViolationOnACleanLine(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "y = 1\n")
	commit(t, env)
	write(t, filepath.Join(env.Dir, "app.py"), old)
	git(t, env.Dir, "add", "app.py")
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	expect(t, infer(t, refs), 0, "")
}

func TestPreCommitRefsSkipANULInTheCommittedBlob(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old+"\x00")
	commit(t, env)
	write(t, filepath.Join(env.Dir, "app.py"), old) // the worktree has the violation; the committed blob does not
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	res := infer(t, refs)
	expect(t, res, 0, skippedText("app.py", "it contains NUL bytes"))
	wantSkippedSummary(t, res, 1)
}

func TestPreCommitRefsWithAnUnknownToRefAreRefused(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	zero := strings.Repeat("0", 40)
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": zero})
	refused(t, infer(t, refs),
		"june: error: pre-commit's to-ref \""+zero+"\" is not a commit in this repository. Fix: fetch it, or pass --base REF or --changed-lines FILE\n")
}

func TestPreCommitRefsWithAnUnknownFromRefAreRefused(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	zero := strings.Repeat("0", 40)
	refs := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": zero, "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	refused(t, infer(t, refs),
		"june: error: the base \""+zero+"\" is not a commit in this repository. Fix: fetch it, or pass an existing branch, tag or commit to --base\n")
}

func TestExplicitInputsWinOverPreCommit(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	commit(t, env)
	hooked := hook(env, map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": revParse(t, env.Dir, "HEAD")})
	if got, want := summaryOf(against(t, hooked, "main")), "; base main (--base)"; !strings.HasSuffix(got, want) {
		t.Errorf("summary = %q, want suffix %q", got, want)
	}
	res, err := check.Change(context.Background(), hooked, check.Request{Changed: strings.NewReader("")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summaryOf(res), "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines"+noPython; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestStagedDiffRunsFromASubdirectory(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"pkg/app.py": "x = 1\n", "other.py": "y = 1\n"})
	write(t, filepath.Join(env.Dir, "pkg", "app.py"), "x = 1\n"+old)
	write(t, filepath.Join(env.Dir, "other.py"), "y = 1\n"+old)
	git(t, env.Dir, "add", "-A")
	copyDir(t, filepath.Join(env.Dir, ".june", "rules"), filepath.Join(env.Dir, "pkg", ".june", "rules"))
	sub := hook(check.Env{Dir: filepath.Join(env.Dir, "pkg")}, nil)
	expect(t, infer(t, sub), 1, "app.py:2:9: "+py012)
}

func TestWithoutPreCommitTheLocalRunIsUnchanged(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+old)
	git(t, env.Dir, "add", "app.py")
	env.Vars = map[string]string{"PRE_COMMIT": "0"}
	if got := string(infer(t, env).Out); !strings.HasPrefix(got, "june: error: no base branch can be inferred") {
		t.Errorf("Out = %q, want the no-base refusal", got)
	}
}
