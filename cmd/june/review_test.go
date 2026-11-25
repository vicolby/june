package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicolby/june/internal/check"
)

const testKey = "sk-proj-CMDTESTKEYCMDTESTKEY"

// fakeAPI is the OpenAI endpoint of these tests: it counts requests and answers with the given status and body.
type fakeAPI struct {
	srv      *httptest.Server
	requests atomic.Int32
	last     atomic.Value // the last request body
}

func newFakeAPI(t *testing.T, status int, body string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.last.Store(string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func answer(findings string) string {
	text, _ := json.Marshal(`{"findings":` + findings + `}`)
	return `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":` + string(text) + `}]}],"usage":{"input_tokens":50,"output_tokens":5}}`
}

// reviewProject is a tree with one Python file and the rules file, run as a GitHub Actions push.
func reviewProject(t *testing.T, api *fakeAPI, extra map[string]string) check.Env {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"app.py":         "x = 1\ndef f(verbose):\n    pass\n",
		"ai-rules.md":    "# Rules\n\n- Do not use a boolean flag parameter.\n",
		"skill/SKILL.md": "---\nname: s\n---\nLook only for flags.\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "push", "OPENAI_API_KEY": testKey}
	if api != nil {
		useOpenAIServer(t, api.srv.URL)
	}
	for k, v := range extra {
		vars[k] = v
	}
	return check.Env{Dir: dir, Vars: vars}
}

func runReview(t *testing.T, env check.Env, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append([]string{"review"}, args...)
	code := run(context.Background(), args, strings.NewReader("app.py:2-3\n"), &out, &errOut, env)
	for _, s := range []string{out.String(), errOut.String()} {
		if strings.Contains(s, testKey) {
			t.Errorf("the key reached the output: %q", s)
		}
	}
	return code, out.String(), errOut.String()
}

var baseArgs = []string{"--rules", "ai-rules.md", "--model", "test-model", "--effort", "low", "--changed-lines", "-", "--format", "text"}

func TestReviewWholeBinaryReportsWarningsAndExitsZero(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[{"path":"app.py","line":2,"reason":"verbose switches behavior"}]`))
	code, out, errOut := runReview(t, reviewProject(t, api, nil), baseArgs...)
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q\n%s", code, errOut, out)
	}
	want := "app.py:2:1: AI verbose switches behavior\njune: 0 errors, 1 warning, 0 opt-outs; checked 1 file, 2 changed lines; ai review test-model effort low: 1 file reviewed, 0 not reviewed, 1 call, 50 tokens in, 5 out\n"
	if out != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out, want)
	}
	body, _ := api.last.Load().(string)
	if !strings.Contains(body, "Do not use a boolean flag parameter.") || !strings.Contains(body, `"model":"test-model"`) || !strings.Contains(body, `"effort":"low"`) {
		t.Errorf("request body = %s", body)
	}
}

func TestReviewSkillAndPromptChangeTheInstruction(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[]`))
	env := reviewProject(t, api, nil)
	if code, out, errOut := runReview(t, env, append(append([]string{}, baseArgs...), "--skill", "skill")...); code != 0 {
		t.Fatalf("exit = %d: %s%s", code, out, errOut)
	}
	if body, _ := api.last.Load().(string); !strings.Contains(body, "Look only for flags.") {
		t.Errorf("the skill is not in the request: %s", body)
	}
	if code, out, errOut := runReview(t, env, append(append([]string{}, baseArgs...), "--prompt", "Only look for PROMPTMARK.")...); code != 0 {
		t.Fatalf("exit = %d: %s%s", code, out, errOut)
	}
	if body, _ := api.last.Load().(string); !strings.Contains(body, "Only look for PROMPTMARK.") {
		t.Errorf("the prompt is not in the request: %s", body)
	}
}

func TestReviewSkillAndPromptTogetherAreRefused(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[]`))
	code, out, errOut := runReview(t, reviewProject(t, api, nil), append(append([]string{}, baseArgs...), "--skill", "skill", "--prompt", "x")...)
	if code != 2 || !strings.Contains(out+errOut, "--skill and --prompt cannot be used together") || api.requests.Load() != 0 {
		t.Errorf("exit = %d, out = %q, err = %q, requests = %d", code, out, errOut, api.requests.Load())
	}
}

func TestReviewEmptyFlagValuesAreAbsent(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[]`))
	args := append(append([]string{}, baseArgs...), "--skill", "", "--prompt", "")
	if code, out, errOut := runReview(t, reviewProject(t, api, nil), args...); code != 0 {
		t.Errorf("exit = %d: %s%s", code, out, errOut)
	}
	// An empty --effort leaves the model's default.
	args = []string{"--rules", "ai-rules.md", "--model", "test-model", "--effort", "", "--changed-lines", "-", "--format", "text"}
	if code, out, errOut := runReview(t, reviewProject(t, api, nil), args...); code != 0 {
		t.Errorf("exit = %d: %s%s", code, out, errOut)
	}
	if body, _ := api.last.Load().(string); strings.Contains(body, `"reasoning"`) {
		t.Errorf("an empty effort was sent: %s", body)
	}
}

func TestReviewRequiresAModelAndRules(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[]`))
	for name, args := range map[string][]string{
		"no model": {"--rules", "ai-rules.md", "--changed-lines", "-"},
		"no rules": {"--model", "m", "--changed-lines", "-"},
	} {
		code, out, errOut := runReview(t, reviewProject(t, api, nil), args...)
		if code != 2 || !strings.Contains(out+errOut, "is required") {
			t.Errorf("%s: exit = %d, out = %q, err = %q", name, code, out, errOut)
		}
	}
	if api.requests.Load() != 0 {
		t.Errorf("%d requests were made", api.requests.Load())
	}
}

func TestReviewWithoutAKeyIsALoudWarningAndMakesNoRequest(t *testing.T) {
	for name, key := range map[string]string{"empty": "", "unset": "\x00"} {
		api := newFakeAPI(t, 200, answer(`[]`))
		env := reviewProject(t, api, nil)
		if key == "" {
			env.Vars["OPENAI_API_KEY"] = ""
		} else {
			delete(env.Vars, "OPENAI_API_KEY")
		}
		code, out, _ := runReview(t, env, baseArgs...)
		if code != 0 || !strings.Contains(out, "june: warning: ai review skipped: OPENAI_API_KEY is not set") || !strings.Contains(out, "app.py") {
			t.Errorf("%s: exit = %d, out =\n%s", name, code, out)
		}
		if api.requests.Load() != 0 {
			t.Errorf("%s: %d requests were made without a key", name, api.requests.Load())
		}
	}
}

func TestReviewABadKeyOrModelIsAWarningWithExitZero(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"bad key":       {401, `{"error":{"message":"Incorrect API key provided: ` + testKey + `"}}`, "the API rejected the credentials (HTTP 401)"},
		"unknown model": {404, `{"error":{"message":"The model nope does not exist"}}`, "the API refused the request (HTTP 404: The model nope does not exist)"},
	} {
		api := newFakeAPI(t, tc.status, tc.body)
		code, out, _ := runReview(t, reviewProject(t, api, nil), baseArgs...)
		if code != 0 || !strings.Contains(out, "june: warning: ai review stopped: "+tc.want) {
			t.Errorf("%s: exit = %d, out =\n%s", name, code, out)
		}
	}
}

func TestReviewRefusesOutsideCIAndMakesNoRequest(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[]`))
	env := reviewProject(t, api, map[string]string{"GITHUB_ACTIONS": "false"})
	code, out, _ := runReview(t, env, baseArgs...)
	if code != 2 || !strings.Contains(out, "runs only in GitHub Actions or GitLab CI") || api.requests.Load() != 0 {
		t.Errorf("exit = %d, out = %q, requests = %d", code, out, api.requests.Load())
	}
}

func TestReviewIgnoresAnEndpointVariable(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[]`))
	env := reviewProject(t, api, map[string]string{"OPENAI_BASE_URL": "http://evil.example/v1"})
	code, out, errOut := runReview(t, env, baseArgs...)
	if code != 0 || errOut != "" || api.requests.Load() != 1 {
		t.Errorf("exit = %d, requests = %d, stderr = %q\n%s", code, api.requests.Load(), errOut, out)
	}
}

// useOpenAIServer makes the process's default HTTP transport serve the fixed OpenAI URL from serverURL.
// june review builds its client with a nil Transport, so this is the request the command actually sends.
func useOpenAIServer(t *testing.T, serverURL string) {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	if realTransport == nil {
		realTransport = http.DefaultTransport
	}
	http.DefaultTransport = roundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" || !strings.HasPrefix(req.URL.Path, "/v1/") {
			t.Errorf("request URL = %s, want https://api.openai.com/v1/...", req.URL)
			return nil, fmt.Errorf("request %s is not the OpenAI API", req.URL)
		}
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		cloned.Host = target.Host
		return realTransport.RoundTrip(cloned)
	})
	t.Cleanup(func() { http.DefaultTransport = realTransport })
}

// realTransport is the process transport captured before these tests replace it.
var realTransport http.RoundTripper

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReviewOnGitLabWritesItsOwnReportFile(t *testing.T) {
	api := newFakeAPI(t, 200, answer(`[{"path":"app.py","line":2,"reason":"verbose switches behavior"}]`))
	env := reviewProject(t, api, map[string]string{"GITLAB_CI": "true", "GITHUB_ACTIONS": ""})
	code, _, errOut := runReview(t, env, "--rules", "ai-rules.md", "--model", "test-model", "--changed-lines", "-")
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	report, err := os.ReadFile(filepath.Join(env.Dir, "gl-code-quality-review-report.json"))
	if err != nil {
		t.Fatalf("review report not written: %v", err)
	}
	if !strings.Contains(string(report), `"check_name": "AI"`) || !strings.Contains(string(report), `"severity": "minor"`) {
		t.Errorf("report = %s", report)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "gl-code-quality-report.json")); !os.IsNotExist(err) {
		t.Errorf("review overwrote the check report (stat err = %v)", err)
	}
}

func TestReviewHelpAndUsage(t *testing.T) {
	code, out, errOut := runArgs(t, check.Env{Dir: t.TempDir()}, "review", "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"usage: june review ", "--rules PATH", "--model", "--effort", "--skill", "--prompt", "OPENAI_API_KEY", "Experimental", "exit codes:"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "OPENAI_BASE_URL") {
		t.Errorf("help still offers an endpoint override:\n%s", out)
	}
	_, general, _ := runArgs(t, check.Env{Dir: t.TempDir()}, "--help")
	if !strings.Contains(general, "june review ") {
		t.Errorf("general help does not list june review")
	}
	code, _, errOut = runArgs(t, check.Env{Dir: t.TempDir()}, "review", "--bogus")
	if code != 2 || !strings.Contains(errOut, "flag provided but not defined: -bogus") {
		t.Errorf("exit = %d, stderr = %q", code, errOut)
	}
	code, _, errOut = runArgs(t, check.Env{Dir: t.TempDir()}, "review", "--rules", "a.md", "--rules", "b.md")
	if code != 2 || !strings.Contains(errOut, "one rules file") {
		t.Errorf("a second --rules: exit = %d, stderr = %q", code, errOut)
	}
	code, _, errOut = runArgs(t, check.Env{Dir: t.TempDir()}, "review", "--format", "bogus")
	if code != 2 || !strings.Contains(errOut, "unknown format") {
		t.Errorf("exit = %d, stderr = %q", code, errOut)
	}
}
