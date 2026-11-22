package check_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
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
	copyDir(t, filepath.Join("..", "..", "testdata", "rules", "python"), filepath.Join(env.Dir, "sub", ".june", "rules"))
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

func TestSARIFUsesRepositoryRootPathsFromASubdirectory(t *testing.T) {
	env := repo(t, map[string]string{"keep.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "sub", "app.py"), old)
	copyDir(t, filepath.Join("..", "..", "testdata", "rules", "python"), filepath.Join(env.Dir, "sub", ".june", "rules"))
	commit(t, env)
	sub := check.Env{Dir: filepath.Join(env.Dir, "sub")}
	res, err := check.Change(context.Background(), sub, check.Request{Changed: strings.NewReader("app.py:1\n"), Format: diag.SARIF})
	if err != nil {
		t.Fatal(err)
	}
	if out := string(res.Out); !strings.Contains(out, `"uri": "sub/app.py"`) {
		t.Errorf("Out =\n%s\nwant the result on sub/app.py", out)
	}
}

// mergeGroup is a merge queue checkout: HEAD is the queue commit on top of the base, and the event payload names that base.
func mergeGroup(t *testing.T) (env check.Env, baseSHA string) {
	t.Helper()
	env = pullRequest(t)
	baseSHA = revParse(t, env.Dir, "origin/main")
	payload := filepath.Join(t.TempDir(), "event.json")
	write(t, payload, `{"merge_group":{"base_sha":"`+baseSHA+`","head_sha":"`+revParse(t, env.Dir, "HEAD")+`"}}`)
	env = onGitHub(env, "merge_group", "")
	env.Vars["GITHUB_EVENT_PATH"] = payload
	return env, baseSHA
}

func TestMergeGroupUsesTheEventBaseSHA(t *testing.T) {
	env, sha := mergeGroup(t)
	// Even if the clone's default branch is somewhere else, the queue judges what it adds to base_sha.
	git(t, env.Dir, "remote", "set-head", "origin", "main")
	res := infer(t, env)
	if res.Exit != 1 {
		t.Errorf("Exit = %d, want 1\n%s", res.Exit, res.Out)
	}
	if want := "; checked 1 file, 1 changed line; base " + sha + " (merge group base)"; !strings.HasSuffix(summaryOf(res), want) {
		t.Errorf("summary = %q, want suffix %q", summaryOf(res), want)
	}
}

func TestMergeGroupWithoutAValidPayloadIsRefused(t *testing.T) {
	for _, fallback := range []string{"default", "upstream"} {
		t.Run(fallback, func(t *testing.T) {
			env, _ := mergeGroup(t)
			git(t, env.Dir, "update-ref", "refs/remotes/origin/queue", "HEAD")
			git(t, env.Dir, "remote", "set-head", "origin", "queue")
			if fallback == "upstream" {
				git(t, env.Dir, "branch", "--set-upstream-to=origin/queue", "feature")
			}
			for _, tc := range []struct{ name, payload string }{
				{"unset-path", ""},
				{"missing-file", ""},
				{"directory", ""},
				{"malformed", "{"},
				{"missing-sha", `{"merge_group":{}}`},
				{"null-group", `{"merge_group":null}`},
				{"empty-sha", `{"merge_group":{"base_sha":""}}`},
				{"wrong-type", `{"merge_group":{"base_sha":123}}`},
				{"invalid-sha", `{"merge_group":{"base_sha":"--upload-pack=x"}}`},
				{"nonhex-sha", `{"merge_group":{"base_sha":"` + strings.Repeat("z", 40) + `"}}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					payload := filepath.Join(t.TempDir(), "event.json")
					if tc.name == "unset-path" {
						payload = ""
					} else if tc.name == "directory" {
						payload = t.TempDir()
					} else if tc.name != "missing-file" {
						write(t, payload, tc.payload)
					}
					env.Vars["GITHUB_EVENT_PATH"] = payload
					if res := infer(t, env); res.Exit != 2 || !strings.Contains(string(res.Out), "the merge group event payload") {
						t.Errorf("Exit = %d, Out =\n%s\nwant a refusal instead of fallback to %s", res.Exit, res.Out, fallback)
					}
				})
			}
		})
	}
}

func TestMergeGroupExplicitScopeDoesNotNeedPayload(t *testing.T) {
	env, _ := mergeGroup(t)
	env.Vars["GITHUB_EVENT_PATH"] = filepath.Join(t.TempDir(), "missing.json")
	results := []check.Result{against(t, env, "origin/main"), change(t, env, "app.py:2\n")}
	env.Vars["PRE_COMMIT_FROM_REF"] = "origin/main"
	env.Vars["PRE_COMMIT_TO_REF"] = "HEAD"
	results = append(results, infer(t, env))
	for _, res := range results {
		if res.Exit != 1 || !strings.Contains(string(res.Out), "\napp.py:2:9: "+py012) {
			t.Errorf("Exit = %d, Out =\n%s", res.Exit, res.Out)
		}
	}
}

func TestMergeGroupBaseThatWasNotFetchedNamesFetchDepth(t *testing.T) {
	env, _ := mergeGroup(t)
	missing := strings.Repeat("a", 40)
	write(t, env.Vars["GITHUB_EVENT_PATH"], `{"merge_group":{"base_sha":"`+missing+`"}}`)
	res := infer(t, env)
	msg := "the merge group base " + missing + " is not a commit in this repository. Fix: set fetch-depth: 0 on actions/checkout so the merge group base is fetched"
	refused(t, res, "::error title=june::"+msg+"\njune: error: "+msg+"\n")
}

func TestPullRequestAtItsBaseIsRefused(t *testing.T) {
	for _, event := range []string{"pull_request", "pull_request_target"} {
		t.Run(event, func(t *testing.T) {
			env := onGitHub(pullRequest(t), event, "main")
			git(t, env.Dir, "checkout", "-q", "--detach", "origin/main")
			for _, base := range []string{"", "origin/main", "HEAD"} {
				name := base
				if name == "" {
					name = "origin/main"
				}
				msg := "HEAD is the base \"" + name + "\" of this pull request, so nothing was judged. Fix: check out the change under review (with pull_request_target, set ref on actions/checkout to the pull request head), or run june on pull_request"
				refused(t, against(t, env, base), "::error title=june::"+msg+"\njune: error: "+msg+"\n")
			}
		})
	}
}

// The PR zero-line contract applies to every scope: empty runs refuse at the
// event base and warn on a feature checkout; supplied nonempty lines still run.
func TestPullRequestSafeguardsApplyAcrossScopes(t *testing.T) {
	for _, event := range []string{"pull_request", "pull_request_target"} {
		t.Run(event, func(t *testing.T) {
			for _, atBase := range []bool{true, false} {
				state := "feature"
				if atBase {
					state = "base"
				}
				t.Run(state, func(t *testing.T) {
					env := onGitHub(pullRequest(t), event, "main")
					if atBase {
						git(t, env.Dir, "checkout", "-q", "--detach", "origin/main")
					}
					for _, list := range []string{"", "README.md:1\n"} {
						res := change(t, env, list)
						if atBase {
							if res.Exit != 2 || !strings.Contains(string(res.Out), "HEAD is the base \"origin/main\"") {
								t.Errorf("supplied %q: Exit = %d, Out =\n%s", list, res.Exit, res.Out)
							}
						} else if res.Exit != 0 || !strings.Contains(string(res.Out), "::warning title=june::this pull request judged 0 changed lines.") {
							t.Errorf("supplied %q: Exit = %d, Out =\n%s", list, res.Exit, res.Out)
						}
					}
					if res := change(t, env, "app.py:1\n"); res.Exit != 1 || strings.Contains(string(res.Out), "judged 0 changed lines") {
						t.Errorf("nonempty supplied lines: Exit = %d, Out =\n%s", res.Exit, res.Out)
					}
					// An empty ref range on a feature checkout must warn, not treat
					// its from-ref HEAD as the event's base and refuse.
					env.Vars["PRE_COMMIT_FROM_REF"] = "HEAD"
					env.Vars["PRE_COMMIT_TO_REF"] = "HEAD"
					res := infer(t, env)
					if atBase {
						if res.Exit != 2 || !strings.Contains(string(res.Out), "HEAD is the base \"origin/main\"") {
							t.Errorf("empty refs: Exit = %d, Out =\n%s", res.Exit, res.Out)
						}
					} else if res.Exit != 0 || !strings.Contains(string(res.Out), "::warning title=june::this pull request judged 0 changed lines.") {
						t.Errorf("empty refs: Exit = %d, Out =\n%s", res.Exit, res.Out)
					}
					env.Vars["PRE_COMMIT_FROM_REF"] = "origin/main"
					res = infer(t, env)
					want := 1
					if atBase {
						want = 2
					}
					if res.Exit != want {
						t.Errorf("event-base refs: Exit = %d, want %d, Out =\n%s", res.Exit, want, res.Out)
					}
				})
			}
		})
	}
}

func TestPullRequestExplicitBaseJudgesChanges(t *testing.T) {
	for _, event := range []string{"pull_request", "pull_request_target"} {
		env := onGitHub(pullRequest(t), event, "main")
		res := against(t, env, "origin/main")
		if res.Exit != 1 || !strings.Contains(string(res.Out), "\napp.py:2:9: "+py012) {
			t.Errorf("%s: Exit = %d, Out =\n%s", event, res.Exit, res.Out)
		}
	}
}

func TestPullRequestExplicitBaseThatJudgesNothingWarns(t *testing.T) {
	for _, event := range []string{"pull_request", "pull_request_target"} {
		t.Run(event, func(t *testing.T) {
			env := onGitHub(pullRequest(t), event, "main")
			git(t, env.Dir, "reset", "-q", "--hard", "origin/main")
			write(t, filepath.Join(env.Dir, "README.md"), "docs\n")
			commit(t, env)
			res := against(t, env, "origin/main")
			if res.Exit != 0 || !strings.Contains(string(res.Out), "::warning title=june::this pull request judged 0 changed lines.") || !strings.Contains(summaryOf(res), "0 errors, 1 warning") {
				t.Errorf("Exit = %d, Out =\n%s", res.Exit, res.Out)
			}
			res, err := check.Change(context.Background(), env, check.Request{Base: "origin/main", Format: diag.SARIF})
			if err != nil {
				t.Fatal(err)
			}
			var log struct {
				Runs []struct {
					Invocations []struct {
						ExecutionSuccessful        bool
						ToolExecutionNotifications []struct{ Level string }
					}
				}
			}
			if err := json.Unmarshal(res.Out, &log); err != nil {
				t.Fatal(err)
			}
			if res.Exit != 0 || len(log.Runs) != 1 || len(log.Runs[0].Invocations) != 1 {
				t.Fatalf("Exit = %d, Out =\n%s", res.Exit, res.Out)
			}
			inv := log.Runs[0].Invocations[0]
			if !inv.ExecutionSuccessful || len(inv.ToolExecutionNotifications) != 1 || inv.ToolExecutionNotifications[0].Level != "warning" {
				t.Errorf("invocation = %+v", inv)
			}
		})
	}
}

func TestMergeGroupThatJudgesNothingPassesWithoutWarning(t *testing.T) {
	for _, state := range []string{"at-base", "docs-only"} {
		t.Run(state, func(t *testing.T) {
			env, sha := mergeGroup(t)
			git(t, env.Dir, "checkout", "-q", "--detach", sha)
			if state == "docs-only" {
				write(t, filepath.Join(env.Dir, "README.md"), "docs\n")
				commit(t, env)
			}
			for _, base := range []string{"", sha, "HEAD"} {
				res := against(t, env, base)
				if res.Exit != 0 || len(res.Diags) != 0 {
					t.Errorf("base %q: Exit = %d, Diags = %+v, Out =\n%s", base, res.Exit, res.Diags, res.Out)
				}
				wantBase, source := base, "--base"
				if base == "" {
					wantBase, source = sha, "merge group base"
				}
				want := "june: 0 errors, 0 warnings, 0 opt-outs; checked 0 files, 0 changed lines; base " + wantBase + " (" + source + ")"
				if summaryOf(res) != want {
					t.Errorf("summary = %q, want %q", summaryOf(res), want)
				}
			}
		})
	}
}

func TestPullRequestThatJudgesNothingWarns(t *testing.T) {
	env := onGitHub(pullRequest(t), "pull_request", "main")
	// A commit with no Python file leaves HEAD past the base with zero lines to judge.
	git(t, env.Dir, "reset", "-q", "--hard", "origin/main")
	write(t, filepath.Join(env.Dir, "README.md"), "docs\n")
	commit(t, env)
	res := infer(t, env)
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0 (a warning, not a failure)\n%s", res.Exit, res.Out)
	}
	out := string(res.Out)
	if want := "::warning title=june::this pull request judged 0 changed lines."; !strings.Contains(out, want) {
		t.Errorf("Out =\n%s\nwant annotation %q", out, want)
	}
	if want := "june: warning: this pull request judged 0 changed lines."; !strings.Contains(out, want) {
		t.Errorf("Out =\n%s\nwant text %q", out, want)
	}
	if want := "june: 0 errors, 1 warning, 0 opt-outs; checked 0 files, 0 changed lines; base origin/main (pull request base)"; summaryOf(res) != want {
		t.Errorf("summary = %q, want %q", summaryOf(res), want)
	}
}

func TestLocalRunThatJudgesNothingDoesNotWarn(t *testing.T) {
	env := pullRequest(t)
	git(t, env.Dir, "branch", "-q", "--set-upstream-to=origin/main", "feature")
	git(t, env.Dir, "reset", "-q", "--hard", "origin/main")
	res := infer(t, env)
	if res.Exit != 0 || strings.Contains(string(res.Out), "judged 0 changed lines") {
		t.Errorf("Exit = %d, Out =\n%s\nwant a quiet pass outside a pull request event", res.Exit, res.Out)
	}
	env.Vars = map[string]string{"GITHUB_EVENT_NAME": "pull_request_target", "GITHUB_BASE_REF": "main"}
	res = against(t, env, "HEAD")
	if res.Exit != 0 || strings.Contains(string(res.Out), "judged 0 changed lines") {
		t.Errorf("local --base HEAD: Exit = %d, Out =\n%s", res.Exit, res.Out)
	}
}
