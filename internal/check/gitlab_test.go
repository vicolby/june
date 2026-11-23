package check_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
)

func head(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// mergeRequest is the environment GitLab gives a merge request pipeline whose diff base is sha.
func mergeRequest(env check.Env, sha string) check.Env {
	vars := map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_DIFF_BASE_SHA": sha}
	for k, v := range env.Vars {
		vars[k] = v
	}
	env.Vars = vars
	return env
}

// bare runs june with no flags, as a CI job does.
func bare(t *testing.T, env check.Env) check.Result {
	t.Helper()
	return infer(t, env)
}

// editedTopic is a repository whose topic branch adds one PY012 violation to app.py after a clean main.
func editedTopic(t *testing.T) check.Env {
	t.Helper()
	env := repo(t, map[string]string{"app.py": old + "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old+"x = 1\ndef g(b={}): pass\n")
	commit(t, env)
	return env
}

// mergedResultCheckout is the finding's history, checked out as GitLab's merged result.
// M1 is a clean main and the diff base. The merge request adds a PY012 to app.py.
// Main then moves to M2, which adds a PY012 on other.py. HEAD is the merge of M2 with the
// merge request, so its first parent is M2.
func mergedResultCheckout(t *testing.T) (env check.Env, diffBase, target string) {
	t.Helper()
	env = repo(t, map[string]string{"app.py": "x = 1\n"})
	diffBase = head(t, env.Dir, "main")
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\ndef g(b={}): pass\n")
	commit(t, env)
	git(t, env.Dir, "checkout", "-q", "main")
	write(t, filepath.Join(env.Dir, "other.py"), "def h(c=[]): pass\n")
	commit(t, env)
	target = head(t, env.Dir, "HEAD")
	git(t, env.Dir, "merge", "-q", "--no-ff", "-m", "merge request", "topic")
	if got := head(t, env.Dir, "HEAD^1"); got != target {
		t.Fatalf("HEAD^1 = %s, want the target branch %s", got, target)
	}
	return env, diffBase, target
}

func TestMergedResultScopeIsTheMergeCommitFirstParent(t *testing.T) {
	t.Parallel()
	env, diffBase, target := mergedResultCheckout(t)
	for _, tc := range []struct {
		name   string
		event  string
		target string // empty leaves CI_MERGE_REQUEST_TARGET_BRANCH_SHA unset
		base   string
	}{
		{"merged result", "merged_result", target, target},
		{"merge train", "merge_train", target, target},
		{"merged result without target sha", "merged_result", "", target},
		{"merge train without target sha", "merge_train", "", target},
		{"merged result with a stale target sha", "merged_result", diffBase, target},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]string{
				"GITLAB_CI":                      "true",
				"CI_MERGE_REQUEST_EVENT_TYPE":    tc.event,
				"CI_MERGE_REQUEST_DIFF_BASE_SHA": diffBase,
			}
			if tc.target != "" {
				vars["CI_MERGE_REQUEST_TARGET_BRANCH_SHA"] = tc.target
			}
			res := bare(t, check.Env{Dir: env.Dir, Vars: vars})
			expect(t, res, 1, "app.py:2:9: "+py012)
			want := "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base " + tc.base + " (merge request diff base)"
			if got := summaryOf(res); got != want {
				t.Errorf("summary = %q, want %q", got, want)
			}
		})
	}
}

// A two-car merge train: car 1 lands a violation on a.py, then car 2's pipeline checks out the merge of car 1's result with its own branch.
// GitLab sets CI_MERGE_REQUEST_TARGET_BRANCH_SHA to the real target tip, which is not the merge commit's first parent.
func TestMergeTrainSecondCarIsNotBlamedForTheFirst(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"a.py": "x = 1\n", "b.py": "y = 1\n"})
	git(t, env.Dir, "checkout", "-q", "main")
	realTarget := head(t, env.Dir, "HEAD")
	git(t, env.Dir, "checkout", "-q", "-b", "car1")
	write(t, filepath.Join(env.Dir, "a.py"), "x = 1\n"+old)
	commit(t, env)
	git(t, env.Dir, "checkout", "-q", "-b", "car2", "main")
	write(t, filepath.Join(env.Dir, "b.py"), "y = 1\ndef g(b={}): pass\n")
	commit(t, env)
	diffBase := head(t, env.Dir, "main")
	git(t, env.Dir, "checkout", "-q", "-b", "train", "main")
	git(t, env.Dir, "merge", "-q", "--no-ff", "-m", "car 1", "car1")
	car1 := head(t, env.Dir, "HEAD")
	git(t, env.Dir, "merge", "-q", "--no-ff", "-m", "car 2", "car2")
	if got := head(t, env.Dir, "HEAD^1"); got != car1 {
		t.Fatalf("HEAD^1 = %s, want car 1's result %s", got, car1)
	}
	vars := map[string]string{
		"GITLAB_CI":                          "true",
		"CI_MERGE_REQUEST_EVENT_TYPE":        "merge_train",
		"CI_MERGE_REQUEST_DIFF_BASE_SHA":     diffBase,
		"CI_MERGE_REQUEST_TARGET_BRANCH_SHA": realTarget,
	}
	res := bare(t, check.Env{Dir: env.Dir, Vars: vars})
	expect(t, res, 1, "b.py:2:9: "+py012)
	want := "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base " + car1 + " (merge request diff base)"
	if got := summaryOf(res); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestDetachedMergeRequestKeepsTheDiffBase(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	tip := head(t, env.Dir, "HEAD")
	env = mergeRequest(env, tip)
	env.Vars["CI_MERGE_REQUEST_EVENT_TYPE"] = "detached"
	env.Vars["CI_MERGE_REQUEST_TARGET_BRANCH_SHA"] = head(t, env.Dir, "main")
	res := bare(t, env)
	expect(t, res, 0, "")
	want := "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines" + noPython + "; base " + tip + " (merge request diff base)"
	if got := summaryOf(res); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestMergeRequestScopeComesFromTheDiffBase(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	res := bare(t, mergeRequest(env, head(t, env.Dir, "main")))
	expect(t, res, 1, "app.py:3:9: "+py012)
	if got, want := summaryOf(res), "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base "+head(t, env.Dir, "main")+" (merge request diff base)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestMergeRequestBaseIsJudgedBeforeAnInferredOne(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	git(t, env.Dir, "branch", "--set-upstream-to=main")
	// The diff base is the topic's own tip, so nothing is touched, though the upstream would blame line 3.
	res := bare(t, mergeRequest(env, head(t, env.Dir, "HEAD")))
	expect(t, res, 0, "")
}

func TestExplicitBaseWinsOverTheMergeRequestBase(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	res, err := check.Change(context.Background(), mergeRequest(env, head(t, env.Dir, "HEAD")), check.Request{Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, res, 1, "app.py:3:9: "+py012)
	if got := summaryOf(res); !strings.HasSuffix(got, "base main (--base)") {
		t.Errorf("summary = %q, want the --base source", got)
	}
}

func TestMergeRequestVariableIsIgnoredOutsideGitLab(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	env.Vars = map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": head(t, env.Dir, "main")}
	res := bare(t, env)
	refused(t, res, "june: error: no base branch can be inferred because the current branch has no upstream and the repository has no remote default branch. Fix: pass --base REF, or --changed-lines FILE\n")
}

func TestMergeRequestBaseMissingFromAShallowCloneNamesFullDepth(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	sha := head(t, env.Dir, "main")
	shallow := filepath.Join(t.TempDir(), "clone")
	git(t, env.Dir, "clone", "-q", "--no-local", "--depth=1", "--branch", "topic", "file://"+env.Dir, shallow)
	res := bare(t, mergeRequest(check.Env{Dir: shallow}, sha))
	refused(t, res, "june: error: the merge request diff base "+sha+" is not a commit in this repository. Fix: clone the full history, with GIT_DEPTH: 0 in the GitLab CI job variables\n")
}

func TestMergeRequestWithoutMergeBaseNamesFullDepth(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	sha := head(t, env.Dir, "main")
	shallow := filepath.Join(t.TempDir(), "clone")
	git(t, env.Dir, "clone", "-q", "--no-local", "--depth=1", "--branch", "topic", "file://"+env.Dir, shallow)
	git(t, shallow, "fetch", "-q", "--depth=1", "origin", "main")
	res := bare(t, mergeRequest(check.Env{Dir: shallow}, sha))
	if res.Exit != 2 || !strings.Contains(string(res.Out), "have no merge base") || !strings.Contains(string(res.Out), "GIT_DEPTH: 0") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestGitLabAddsACodeQualityReportBesideTheSameText(t *testing.T) {
	t.Parallel()
	env := editedTopic(t)
	sha := head(t, env.Dir, "main")
	plain, err := check.Change(context.Background(), env, check.Request{Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	res := bare(t, mergeRequest(env, sha))
	if res.Exit != plain.Exit {
		t.Errorf("Exit = %d, want the dev-shell %d", res.Exit, plain.Exit)
	}
	if findingsOf(t, res) != findingsOf(t, plain) {
		t.Errorf("findings differ from the dev shell:\n%s\nvs\n%s", findingsOf(t, res), findingsOf(t, plain))
	}
	if len(plain.Files) != 0 {
		t.Errorf("a dev shell run wrote files %v", plain.Files)
	}
	if len(res.Files) != 1 || res.Files[0].Path != diag.GitLabReport || !strings.Contains(string(res.Files[0].Data), `"check_name": "PY012"`) {
		t.Errorf("Files = %v, want the Code Quality report naming PY012", res.Files)
	}
}

func TestGitLabCleanRunStillWritesAnEmptyReport(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	res := bare(t, mergeRequest(env, head(t, env.Dir, "main")))
	expect(t, res, 0, "")
	if len(res.Files) != 1 || string(res.Files[0].Data) != "[]\n" {
		t.Errorf("Files = %v, want an empty report", res.Files)
	}
}

func TestGitLabRefusalWritesNoReport(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	res := bare(t, mergeRequest(env, strings.Repeat("0", 40)))
	if res.Exit != 2 || len(res.Files) != 0 {
		t.Errorf("Exit = %d, Files = %v, want a refusal with no report", res.Exit, res.Files)
	}
}

func TestGitLabSuppliedListAlsoWritesTheReport(t *testing.T) {
	t.Parallel()
	env := mergeRequest(tree(t, map[string]string{"app.py": old}), "")
	delete(env.Vars, "CI_MERGE_REQUEST_DIFF_BASE_SHA")
	res := change(t, env, "app.py:1\n")
	expect(t, res, 1, "app.py:1:9: "+py012)
	if len(res.Files) != 1 {
		t.Errorf("Files = %v, want the report", res.Files)
	}
}
