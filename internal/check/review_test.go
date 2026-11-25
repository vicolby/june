package check_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
	"github.com/vicolby/june/internal/review"
	"github.com/vicolby/june/internal/review/reviewtest"
)

const aiRules = "# Rules\n\n- Do not switch behavior on a boolean parameter.\n"

// inCI marks env as a GitHub Actions push run, where june review is allowed.
func inCI(env check.Env, extra ...string) check.Env {
	vars := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "push"}
	for i := 0; i+1 < len(extra); i += 2 {
		vars[extra[i]] = extra[i+1]
	}
	env.Vars = vars
	return env
}

func reviewTree(t *testing.T, files map[string]string) check.Env {
	t.Helper()
	env := tree(t, files)
	write(t, filepath.Join(env.Dir, "ai-rules.md"), aiRules)
	return env
}

func reviewReq(m review.Model, list string) check.ReviewRequest {
	req := check.ReviewRequest{
		Format: diag.Text,
		AI:     review.Config{Rules: "ai-rules.md", Model: "test-model", Effort: "low"},
		Model:  m,
	}
	if list != "" {
		req.Changed = strings.NewReader(list)
	}
	return req
}

func doReview(t *testing.T, env check.Env, req check.ReviewRequest) check.Result {
	t.Helper()
	res, err := check.Review(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReviewSendsANewlineOnlyFileAndCountsLinesItCannotSend(t *testing.T) {
	t.Parallel()
	t.Run("a file of only a newline", func(t *testing.T) {
		t.Parallel()
		for name, body := range map[string]string{"lf": "\n", "crlf": "\r\n"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				env := inCI(reviewTree(t, map[string]string{"app.py": body}))
				fake := reviewtest.NewFake(reviewtest.Reply())
				res := doReview(t, env, reviewReq(fake, "app.py:1\n"))
				if res.Exit != 0 || len(fake.Queries()) != 1 {
					t.Fatalf("Exit = %d, calls = %d\n%s", res.Exit, len(fake.Queries()), res.Out)
				}
				if !strings.Contains(fake.Queries()[0].Input, "> 1| \n") {
					t.Errorf("input =\n%s", fake.Queries()[0].Input)
				}
				if got := summaryOf(res); !strings.Contains(got, "checked 1 file, 1 changed line") || !strings.Contains(got, "1 file reviewed, 0 not reviewed") {
					t.Errorf("summary = %q", got)
				}
			})
		}
	})
	t.Run("a changed line past the end of the file", func(t *testing.T) {
		t.Parallel()
		env := inCI(reviewTree(t, map[string]string{"app.py": "x = 1\n"}))
		fake := reviewtest.NewFake()
		res := doReview(t, env, reviewReq(fake, "app.py:50\n"))
		if res.Exit != 0 || len(fake.Queries()) != 0 {
			t.Fatalf("Exit = %d, calls = %d\n%s", res.Exit, len(fake.Queries()), res.Out)
		}
		if !strings.Contains(string(res.Out), "app.py") || !strings.Contains(string(res.Out), "not in the file") {
			t.Errorf("Out =\n%s", res.Out)
		}
		if got := summaryOf(res); !strings.Contains(got, "checked 1 file, 1 changed line") || !strings.Contains(got, "0 files reviewed, 1 not reviewed") {
			t.Errorf("summary = %q", got)
		}
	})
}

func TestReviewReportsAFindingAsAWarningWithExitZero(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"app.py": "def f(a):\n    return a\n\ndef g(verbose):\n    pass\n"}))
	fake := reviewtest.NewFake(reviewtest.Reply(reviewtest.Finding{Path: "app.py", Line: 4, Reason: "`verbose` selects between two behaviors."}))
	res := doReview(t, env, reviewReq(fake, "app.py:4-5\n"))
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0: a finding is only a warning", res.Exit)
	}
	want := "app.py:4:1: AI `verbose` selects between two behaviors.\n"
	if got := findingsOf(t, res); got != want {
		t.Errorf("findings =\n%s\nwant\n%s", got, want)
	}
	wantSum := "june: 0 errors, 1 warning, 0 opt-outs; checked 1 file, 2 changed lines; ai review test-model effort low: 1 file reviewed, 0 not reviewed, 1 call, 100 tokens in, 10 out"
	if got := summaryOf(res); got != wantSum {
		t.Errorf("summary = %q\nwant      %q", got, wantSum)
	}
	for _, d := range res.Diags {
		if d.Sev != diag.Warning {
			t.Errorf("diagnostic %+v is not a warning", d)
		}
	}
}

func TestReviewSendsTheRulesAndMarksOnlyTheTouchedLines(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"app.py": "a = 1\nb = 2\nc = 3\n"}))
	fake := reviewtest.NewFake(reviewtest.Reply())
	doReview(t, env, reviewReq(fake, "app.py:2\n"))
	in := fake.Queries()[0].Input
	for _, want := range []string{aiRules, "FILE app.py\n", "  1| a = 1\n", "> 2| b = 2\n", "  3| c = 3\n"} {
		if !strings.Contains(in, want) {
			t.Errorf("input lacks %q:\n%s", want, in)
		}
	}
}

func TestReviewJudgesTheSameLinesAsABareRunForTheSameBase(t *testing.T) {
	t.Parallel()
	env := inCI(repo(t, map[string]string{"app.py": old + "x = 1\n"}))
	write(t, filepath.Join(env.Dir, "ai-rules.md"), aiRules)
	write(t, filepath.Join(env.Dir, "app.py"), old+"x = 1\ndef g(b={}): pass\n")
	commit(t, env)
	fake := reviewtest.NewFake(reviewtest.Reply())
	req := reviewReq(fake, "")
	req.Base = "main"
	res := doReview(t, env, req)
	in := fake.Queries()[0].Input
	if !strings.Contains(in, "> 3| def g(b={}): pass\n") || strings.Contains(in, ">  1|") || strings.Contains(in, "> 1|") {
		t.Errorf("input =\n%s\nwant only line 3 marked", in)
	}
	bare := against(t, env, "main")
	if !strings.Contains(summaryOf(bare), "checked 1 file, 1 changed line") || !strings.Contains(summaryOf(res), "checked 1 file, 1 changed line; base main (--base)") {
		t.Errorf("summaries differ:\nreview: %s\nbare:   %s", summaryOf(res), summaryOf(bare))
	}
}

func TestReviewOnlyRunsInCI(t *testing.T) {
	t.Parallel()
	for name, vars := range map[string]map[string]string{
		"a developer shell":     nil,
		"pre-commit":            {"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "push", "PRE_COMMIT": "1"},
		"pull_request_target":   {"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "pull_request_target"},
		"another CI":            {"CI": "true", "JENKINS_URL": "x"},
		"GITLAB_CI that is off": {"GITLAB_CI": "false"},
	} {
		t.Run(name, func(t *testing.T) {
			env := reviewTree(t, map[string]string{"app.py": "x = 1\n"})
			env.Vars = vars
			fake := reviewtest.NewFake(reviewtest.Reply())
			res := doReview(t, env, reviewReq(fake, "app.py:1\n"))
			if res.Exit != 2 {
				t.Errorf("Exit = %d, want 2", res.Exit)
			}
			out := string(res.Out)
			if !strings.HasPrefix(out, "june: error: ") || !strings.Contains(out, ". Fix: ") {
				t.Errorf("Out = %q, want a refusal with a fix", out)
			}
			if n := len(fake.Queries()); n != 0 {
				t.Errorf("the model was called %d times", n)
			}
		})
	}
	t.Run("GitLab CI", func(t *testing.T) {
		env := reviewTree(t, map[string]string{"app.py": "x = 1\n"})
		env.Vars = map[string]string{"GITLAB_CI": "true"}
		res := doReview(t, env, reviewReq(reviewtest.NewFake(reviewtest.Reply()), "app.py:1\n"))
		if res.Exit != 0 {
			t.Errorf("Exit = %d, want 0\n%s", res.Exit, res.Out)
		}
	})
}

func TestReviewRefusesBadConfigurationBeforeAnyCall(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"app.py": "x = 1\n"}))
	for name, mutate := range map[string]func(*check.ReviewRequest){
		"no model":            func(r *check.ReviewRequest) { r.AI.Model = "" },
		"skill and prompt":    func(r *check.ReviewRequest) { r.AI.Skill, r.AI.Prompt = "ai-rules.md", "x" },
		"rules file missing":  func(r *check.ReviewRequest) { r.AI.Rules = "nope.md" },
		"changed and ignored": func(r *check.ReviewRequest) { r.IgnoreSpace = true },
	} {
		t.Run(name, func(t *testing.T) {
			fake := reviewtest.NewFake(reviewtest.Reply())
			req := reviewReq(fake, "app.py:1\n")
			mutate(&req)
			res := doReview(t, env, req)
			if res.Exit != 2 || !strings.HasPrefix(string(res.Out), "june: error: ") || !strings.Contains(string(res.Out), ". Fix: ") {
				t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
			}
			if len(fake.Queries()) != 0 {
				t.Errorf("the model was called")
			}
		})
	}
}

func TestReviewRefusesWhenThereIsNoScopeJustAsABareRunDoes(t *testing.T) {
	t.Parallel()
	env := inCI(pushCheckout(t), "GITHUB_EVENT_NAME", "push")
	write(t, filepath.Join(env.Dir, "ai-rules.md"), aiRules)
	bare, err := check.Change(context.Background(), env, check.Request{Format: diag.Text})
	if err != nil {
		t.Fatal(err)
	}
	res := doReview(t, env, reviewReq(reviewtest.NewFake(reviewtest.Reply()), ""))
	if res.Exit != 2 || string(res.Out) != string(bare.Out) {
		t.Errorf("review: exit %d, %q\nbare:   exit %d, %q", res.Exit, res.Out, bare.Exit, bare.Out)
	}
}

func TestReviewNeedsNeitherRulesNorAstGrep(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bin := t.TempDir()
	if err := os.Symlink(gitBin, filepath.Join(bin, "git")); err != nil {
		t.Skip("cannot link git: " + err.Error())
	}
	t.Setenv("PATH", bin) // git and nothing else: no ast-grep, and no rules directory either
	dir := t.TempDir()
	write(t, filepath.Join(dir, "app.py"), "x = 1\n")
	write(t, filepath.Join(dir, "ai-rules.md"), aiRules)
	res := doReview(t, inCI(check.Env{Dir: dir}), reviewReq(reviewtest.NewFake(reviewtest.Reply()), "app.py:1\n"))
	if res.Exit != 0 {
		t.Errorf("Exit = %d\n%s", res.Exit, res.Out)
	}
}

func TestReviewWithNothingToJudgeMakesNoCall(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"app.py": "x = 1\n", "notes.txt": "hi\n"}))
	fake := reviewtest.NewFake()
	res := doReview(t, env, reviewReq(fake, "notes.txt:1\n"))
	if res.Exit != 0 || len(fake.Queries()) != 0 {
		t.Errorf("Exit = %d, calls = %d", res.Exit, len(fake.Queries()))
	}
	if got := summaryOf(res); !strings.Contains(got, "checked 0 files, 0 changed lines") || !strings.Contains(got, "no changed Python files found") || !strings.Contains(got, "ai review test-model effort low: 0 files reviewed") {
		t.Errorf("summary = %q", got)
	}
}

func TestReviewKeepsExcludedAndUnnamedFilesOutOfTheRequest(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{
		"app.py":          "x = 1\n",
		"gen/schema.py":   "SECRET_TOKEN = 1\n",
		"other/thing.py":  "y = 2\n",
		"other/deeper.py": "z = 3\n",
	}))
	list := "app.py:1\ngen/schema.py:1\nother/thing.py:1\nother/deeper.py:1\n"
	fake := reviewtest.NewFake(reviewtest.Reply())
	req := reviewReq(fake, list)
	req.Exclude = []string{"gen/**"}
	req.Paths = []string{"app.py", "gen/schema.py", "other"}
	res := doReview(t, env, req)
	in := fake.Queries()[0].Input
	for _, want := range []string{"FILE app.py", "FILE other/thing.py", "FILE other/deeper.py"} {
		if !strings.Contains(in, want) {
			t.Errorf("input lacks %q", want)
		}
	}
	if strings.Contains(in, "gen/schema.py") || strings.Contains(in, "SECRET_TOKEN") {
		t.Errorf("an excluded file was sent:\n%s", in)
	}
	if !strings.Contains(summaryOf(res), "1 file excluded") {
		t.Errorf("summary = %q, want the excluded count", summaryOf(res))
	}

	fake = reviewtest.NewFake(reviewtest.Reply())
	req = reviewReq(fake, list)
	req.Paths = []string{"other/thing.py"}
	doReview(t, env, req)
	if in := fake.Queries()[0].Input; strings.Contains(in, "app.py") || strings.Contains(in, "deeper.py") || !strings.Contains(in, "thing.py") {
		t.Errorf("PATH did not narrow the request:\n%s", in)
	}
}

func TestReviewDecodesDeclaredEncodingsAndNamesAFileItCannotRead(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"bad.py": "x = 1\n"}))
	write(t, filepath.Join(env.Dir, "latin.py"), "# coding: latin-1\nname = 'caf\xe9'\n")
	write(t, filepath.Join(env.Dir, "bad.py"), "name = 'caf\xe9'\n")
	fake := reviewtest.NewFake(reviewtest.Reply())
	res := doReview(t, env, reviewReq(fake, "bad.py:1\nlatin.py:1-2\n"))
	in := fake.Queries()[0].Input
	if !strings.Contains(in, "name = 'café'") || strings.Contains(in, "\xe9") {
		t.Errorf("latin-1 file was not decoded:\n%q", in)
	}
	if strings.Contains(in, "FILE bad.py") {
		t.Errorf("an unreadable file was sent")
	}
	if !strings.Contains(string(res.Out), skippedText("bad.py", "it is not valid UTF-8 and declares no other encoding")) {
		t.Errorf("Out =\n%s", res.Out)
	}
	wantSkippedSummary(t, res, 1)
	if res.Exit != 0 {
		t.Errorf("Exit = %d", res.Exit)
	}
}

func TestReviewTurnsAFailedCallIntoAWarningAndExitZero(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"app.py": "x = 1\n"}))
	fake := reviewtest.NewFake(reviewtest.Fail(&review.CallError{Kind: review.Auth, Status: 401}))
	res := doReview(t, env, reviewReq(fake, "app.py:1\n"))
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0", res.Exit)
	}
	out := string(res.Out)
	if !strings.Contains(out, "june: warning: ai review stopped: the API rejected the credentials (HTTP 401)") || !strings.Contains(out, "app.py") {
		t.Errorf("Out =\n%s", out)
	}
	if got := summaryOf(res); !strings.Contains(got, "1 warning") || !strings.Contains(got, "0 files reviewed, 1 not reviewed") {
		t.Errorf("summary = %q", got)
	}
}

func TestReviewRendersInEveryFormat(t *testing.T) {
	t.Parallel()
	files := map[string]string{"app.py": "def f(a):\n    return a\n"}
	reply := reviewtest.Reply(reviewtest.Finding{Path: "app.py", Line: 2, Reason: "returns early | maybe"})
	run := func(t *testing.T, env check.Env, f diag.Format) check.Result {
		req := reviewReq(reviewtest.NewFake(reply), "app.py:2\n")
		req.Format = f
		return doReview(t, env, req)
	}
	t.Run("github", func(t *testing.T) {
		stepSummary := filepath.Join(t.TempDir(), "summary.md")
		env := inCI(reviewTree(t, files), "GITHUB_STEP_SUMMARY", stepSummary)
		res := run(t, env, diag.GitHub)
		if want := "::warning file=app.py,line=2,col=1,title=AI::returns early | maybe\n"; !strings.HasPrefix(string(res.Out), want) {
			t.Errorf("Out =\n%s\nwant it to start with %q", res.Out, want)
		}
		var step []byte
		for _, f := range res.Files {
			if f.Path == stepSummary {
				step = f.Data
			}
		}
		if !strings.HasPrefix(string(step), "## june review\n\n") {
			t.Errorf("job summary = %q, want the june review heading", step)
		}
	})
	t.Run("gitlab", func(t *testing.T) {
		env := reviewTree(t, files)
		env.Vars = map[string]string{"GITLAB_CI": "true"}
		res := run(t, env, diag.GitLab)
		if len(res.Files) != 1 || res.Files[0].Path != "gl-code-quality-review-report.json" {
			t.Fatalf("files = %v, want gl-code-quality-review-report.json only", res.Files)
		}
		type entry struct {
			CheckName   string `json:"check_name"`
			Severity    string `json:"severity"`
			Fingerprint string `json:"fingerprint"`
			Location    struct {
				Path  string `json:"path"`
				Lines struct {
					Begin int `json:"begin"`
				} `json:"lines"`
			} `json:"location"`
		}
		var got []entry
		if err := json.Unmarshal(res.Files[0].Data, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Severity != "minor" || got[0].CheckName != "AI" || got[0].Location.Path != "app.py" || got[0].Location.Lines.Begin != 2 {
			t.Errorf("report = %+v", got)
		}
	})
	t.Run("json and sarif", func(t *testing.T) {
		env := inCI(reviewTree(t, files))
		for _, f := range []diag.Format{diag.JSON, diag.SARIF} {
			res := run(t, env, f)
			if !json.Valid(res.Out) || !strings.Contains(string(res.Out), "returns early") {
				t.Errorf("%s output = %s", f, res.Out)
			}
		}
	})
}

func TestReviewFingerprintsIgnoreTheModelsWording(t *testing.T) {
	t.Parallel()
	env := reviewTree(t, map[string]string{"app.py": "x = 1\n"})
	env.Vars = map[string]string{"GITLAB_CI": "true"}
	prints := func(reason string) string {
		req := reviewReq(reviewtest.NewFake(reviewtest.Reply(reviewtest.Finding{Path: "app.py", Line: 1, Reason: reason})), "app.py:1\n")
		req.Format = diag.GitLab
		res := doReview(t, env, req)
		var got []struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(res.Files[0].Data, &got); err != nil || len(got) != 1 {
			t.Fatalf("report = %s (%v)", res.Files[0].Data, err)
		}
		return got[0].Fingerprint
	}
	if a, b := prints("first wording"), prints("a different wording"); a != b {
		t.Errorf("fingerprints differ: %s vs %s", a, b)
	}
}

func TestReviewStopsOnCancellationAndRejectsAnEmptyDir(t *testing.T) {
	t.Parallel()
	env := inCI(reviewTree(t, map[string]string{"app.py": "x = 1\n"}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := check.Review(ctx, env, reviewReq(reviewtest.NewFake(reviewtest.Reply()), "app.py:1\n")); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, err := check.Review(context.Background(), check.Env{}, reviewReq(nil, "")); err == nil {
		t.Errorf("an empty Env.Dir must be an error")
	}
}

func TestReviewAtThePullRequestBaseIsRefusedAndAnEmptyPullRequestWarns(t *testing.T) {
	t.Parallel()
	env := onGitHub(pullRequest(t), "pull_request", "main")
	write(t, filepath.Join(env.Dir, "ai-rules.md"), aiRules)

	// A commit with no Python file leaves HEAD past the base with zero lines to judge.
	git(t, env.Dir, "reset", "-q", "--hard", "origin/main")
	write(t, filepath.Join(env.Dir, "README.md"), "docs\n")
	commit(t, env)
	fake := reviewtest.NewFake(reviewtest.Reply())
	res := doReview(t, env, reviewReq(fake, ""))
	if res.Exit != 0 || len(fake.Queries()) != 0 {
		t.Errorf("Exit = %d, calls = %d", res.Exit, len(fake.Queries()))
	}
	if !strings.Contains(string(res.Out), "june: warning: this pull request judged 0 changed lines.") {
		t.Errorf("Out =\n%s", res.Out)
	}

	git(t, env.Dir, "checkout", "-q", "--detach", "origin/main")
	write(t, filepath.Join(env.Dir, "ai-rules.md"), aiRules)
	res = doReview(t, env, reviewReq(fake, ""))
	if res.Exit != 2 || !strings.Contains(string(res.Out), `HEAD is the base "origin/main" of this pull request, so nothing was judged.`) {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}
