package check_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

// onGitHub returns env with the variables GitHub Actions sets for the given event.
func onGitHub(env check.Env, event, baseRef string) check.Env {
	vars := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": event}
	if baseRef != "" {
		vars["GITHUB_BASE_REF"] = baseRef
	}
	for k, v := range env.Vars {
		vars[k] = v
	}
	env.Vars = vars
	return env
}

// pullRequest is a full clone on an untracked feature branch that adds one violation, as a PR checkout with fetch-depth 0 does.
func pullRequest(t *testing.T) check.Env {
	t.Helper()
	origin := repo(t, map[string]string{"app.py": old})
	env := clone(t, origin)
	git(t, env.Dir, "checkout", "-q", "-b", "feature", "--no-track", "origin/main")
	git(t, env.Dir, "remote", "set-head", "origin", "-d")
	write(t, filepath.Join(env.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, env)
	return env
}

func TestPullRequestScopeComesFromTheEventBase(t *testing.T) {
	env := onGitHub(pullRequest(t), "pull_request", "main")
	res := infer(t, env)
	if res.Exit != 1 {
		t.Errorf("Exit = %d, want 1", res.Exit)
	}
	out := string(res.Out)
	if !strings.Contains(out, "\napp.py:2:9: "+py012) {
		t.Errorf("Out =\n%s\nwant the plain finding line", out)
	}
	if want := "june: 1 error, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line; base origin/main (pull request base)\n"; !strings.Contains(out, want) {
		t.Errorf("Out =\n%s\nwant summary %q", out, want)
	}
}

func TestPullRequestTargetUsesTheSameBase(t *testing.T) {
	env := onGitHub(pullRequest(t), "pull_request_target", "main")
	if got, want := summaryOf(infer(t, env)), "; base origin/main (pull request base)"; !strings.HasSuffix(got, want) {
		t.Errorf("summary = %q, want suffix %q", got, want)
	}
}

func TestPullRequestEmitsAnnotationsBesidePlainText(t *testing.T) {
	env := pullRequest(t)
	git(t, env.Dir, "branch", "-q", "--set-upstream-to=origin/main", "feature")
	plain := infer(t, env)
	gh := infer(t, onGitHub(env, "pull_request", "main"))
	if gh.Exit != plain.Exit {
		t.Errorf("Exit = %d, want %d as in a dev shell", gh.Exit, plain.Exit)
	}
	annotation := "::error file=app.py,line=2,col=9,title=PY012::"
	if !strings.Contains(string(gh.Out), annotation) {
		t.Errorf("Out =\n%s\nwant an annotation starting %q", gh.Out, annotation)
	}
}

func TestPullRequestWinsOverNothingButExplicitScope(t *testing.T) {
	env := onGitHub(pullRequest(t), "pull_request", "main")
	git(t, env.Dir, "branch", "-q", "release", "origin/main")
	res, err := check.Change(context.Background(), env, check.Request{Base: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summaryOf(res), "; base release (--base)"; !strings.HasSuffix(got, want) {
		t.Errorf("summary = %q, want suffix %q", got, want)
	}
	res, err = check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("app.py:1\n")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Out), "base ") {
		t.Errorf("a supplied list names a base: %s", res.Out)
	}
}

func TestPullRequestBaseThatWasNotFetchedNamesFetchDepth(t *testing.T) {
	env := onGitHub(pullRequest(t), "pull_request", "gone")
	res := infer(t, env)
	msg := "the base \"origin/gone\" is not a commit in this repository. Fix: set fetch-depth: 0 on actions/checkout so the pull request base is fetched, or pass an existing branch, tag or commit to --base"
	refused(t, res, "::error title=june::"+msg+"\njune: error: "+msg+"\n")
}

func TestPushEventWithoutABaseIsRefusedWithANamedFix(t *testing.T) {
	env := onGitHub(pullRequest(t), "push", "")
	res := infer(t, env)
	msg := "a push event has no pull request base, so the touched lines cannot be found. Fix: pass --base REF (for example --base origin/main) with fetch-depth: 0 on actions/checkout, or run june on pull_request"
	refused(t, res, "::error title=june::"+msg+"\njune: error: "+msg+"\n")
}

func TestPushEventWithAnExplicitBaseIsJudged(t *testing.T) {
	env := onGitHub(pullRequest(t), "push", "")
	if res := against(t, env, "origin/main"); res.Exit != 1 {
		t.Errorf("Exit = %d, want 1\n%s", res.Exit, res.Out)
	}
}

func TestOutsideGitHubAPullRequestVariableIsIgnored(t *testing.T) {
	env := pullRequest(t)
	env.Vars = map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_BASE_REF": "main"}
	refused(t, infer(t, env),
		"june: error: no base branch can be inferred because the current branch has no upstream and the repository has no remote default branch. Fix: pass --base REF, or --changed-lines FILE\n")
}

func TestTextIsTheSameInADevShellAndOnGitHub(t *testing.T) {
	env := pullRequest(t)
	git(t, env.Dir, "branch", "-q", "--set-upstream-to=origin/main", "feature")
	plain := infer(t, env)
	gh := infer(t, onGitHub(env, "pull_request", "main"))
	var text []string
	for _, l := range strings.SplitAfter(string(gh.Out), "\n") {
		if !strings.HasPrefix(l, "::") {
			text = append(text, l)
		}
	}
	// Only the named source of the base differs; the rest of the text is byte for byte the same.
	want := strings.Replace(string(plain.Out), "(upstream branch)", "(pull request base)", 1)
	if got := strings.Join(text, ""); got != want {
		t.Errorf("text differs:\n%s\nvs\n%s", got, want)
	}
}

// pushCheckout is what actions/checkout leaves on a push event: the branch created from origin/<branch>, tracking it, with HEAD equal to it.
func pushCheckout(t *testing.T) check.Env {
	t.Helper()
	origin := repo(t, map[string]string{"app.py": old})
	git(t, origin.Dir, "checkout", "-q", "main")
	write(t, filepath.Join(origin.Dir, "app.py"), old+"def g(b={}): pass\n")
	commit(t, origin)
	env := clone(t, origin)
	git(t, env.Dir, "checkout", "-q", "-b", "feature", "--track", "origin/main")
	return onGitHub(env, "push", "")
}

func TestPushEventIsRefusedEvenWhenTheBranchTracksItsOwnRemote(t *testing.T) {
	env := pushCheckout(t)
	res := infer(t, env)
	msg := "a push event has no pull request base, so the touched lines cannot be found. Fix: pass --base REF (for example --base origin/main) with fetch-depth: 0 on actions/checkout, or run june on pull_request"
	refused(t, res, "::error title=june::"+msg+"\njune: error: "+msg+"\n")
}

func TestPushEventStillHonoursAnExplicitBaseAndList(t *testing.T) {
	env := pushCheckout(t)
	git(t, env.Dir, "branch", "-q", "older", "HEAD~1")
	if res := against(t, env, "HEAD~1"); res.Exit != 1 {
		t.Errorf("--base: Exit = %d, want 1\n%s", res.Exit, res.Out)
	}
	if res := change(t, env, "app.py:2\n"); res.Exit != 1 {
		t.Errorf("list: Exit = %d, want 1\n%s", res.Exit, res.Out)
	}
}

func TestAnnotationsUseRepositoryRootPathsFromASubdirectory(t *testing.T) {
	env := repo(t, map[string]string{"keep.py": "x = 1\n"})
	git(t, env.Dir, "remote", "add", "origin", env.Dir)
	git(t, env.Dir, "fetch", "-q", "origin")
	write(t, filepath.Join(env.Dir, "sub", "app.py"), old)
	copyDir(t, filepath.Join("..", "..", "rules", "python"), filepath.Join(env.Dir, "sub", ".june", "rules"))
	commit(t, env)
	sub := onGitHub(check.Env{Dir: filepath.Join(env.Dir, "sub")}, "pull_request", "main")
	for name, res := range map[string]check.Result{
		"pull request": infer(t, sub),
		"list":         change(t, sub, "app.py:1\n"),
	} {
		out := string(res.Out)
		if !strings.Contains(out, "::error file=sub/app.py,line=1,col=9,title=PY012::") {
			t.Errorf("%s: Out =\n%s\nwant an annotation on sub/app.py", name, out)
		}
		if !strings.Contains(out, "\napp.py:1:9: "+py012) {
			t.Errorf("%s: Out =\n%s\nwant the plain text relative to the working directory", name, out)
		}
	}
}
