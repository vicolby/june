// Package check decides one change: which lines are touched, which findings land on them, and the exit code.
package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/vicolby/june/internal/diag"
	"github.com/vicolby/june/internal/rules"
)

const defaultRules = ".june/rules"

// Env is a snapshot of the caller's environment; nothing here reads ambient state.
type Env struct {
	Dir  string
	Vars map[string]string
}

// Request describes one change to judge.
type Request struct {
	// Base is a git ref; the touched lines are those changed since its merge base with HEAD,
	// worktree changes and untracked files included. Changed wins when both are set.
	Base string
	// Changed is the supplied touched-line list, one "path:7" or "path:7-12" per line.
	Changed io.Reader
	// Rules lists the rules directories, merged into one set; empty means ./.june/rules.
	// A directory named twice counts once, and a rule id defined in two files is a rule fault.
	Rules []string
	// Format is the output format, one of diag's; empty is inferred: GitHub under GitHub Actions, GitLab under GitLab CI, else text.
	Format diag.Format
	// Legacy also lists rule matches on untouched lines of the checked files, as information only.
	// They never change Exit; off by default.
	Legacy bool

	// root is the working directory's path under the repository root, with a trailing slash, for native formats.
	root string
	// stepSummary is the path of the GitHub job summary, "" when there is none.
	stepSummary string
}

// Result is the verdict and its rendering.
type Result struct {
	Exit  int
	Out   []byte
	Files []diag.File
	Diags []diag.Diagnostic
}

// Change judges one change. Exit is 1 if and only if an unsuppressed error-severity match is anchored on a touched line.
// Exit 2 means june could not produce a verdict and Out says why.
// An empty Env.Dir or cancellation returns an error.
func Change(ctx context.Context, env Env, req Request) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	req.Format = formatFor(env, req)
	req.root = repoPrefix(ctx, env, req.Format)
	req.stepSummary = env.Vars["GITHUB_STEP_SUMMARY"]
	touched, sc, bad, err := findTouched(ctx, env, req)
	if sc.scanDir != "" {
		defer func() { _ = os.RemoveAll(sc.scanDir) }()
	}
	if err != nil {
		return Result{}, err
	}
	if bad != nil {
		return refuse(req, *bad), nil
	}

	rs, refusal, err := load(ctx, env, req.Rules, req)
	if refusal != nil {
		return *refusal, nil
	}
	if err != nil {
		return Result{}, err
	}

	root := env.Dir
	if sc.scanDir != "" {
		root = sc.scanDir
	}
	files := touched.pythonFiles()
	scanned, err := rs.Scan(ctx, root, files)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		var notText *rules.NotTextError
		if errors.As(err, &notText) {
			// Scan reports the slash-separated path it was given, already relative to the working directory.
			return refuse(req, fmt.Sprintf("%q is not UTF-8 text, so it cannot be scanned. Fix: save it as UTF-8 without NUL bytes and run june again", notText.File)), nil
		}
		return refuse(req, "ast-grep failed while scanning ("+err.Error()+"). Fix: run ast-grep scan on the changed files to see why"), nil
	}

	var ds []diag.Diagnostic
	exit := 0
	var errs, warns, optOutCount int
	type site struct {
		file string
		line int
		rule string
	}
	optedOut := map[site]bool{}
	found := map[site]bool{}
	legacyOptOut := map[site]bool{} // valid opt-outs on untouched lines hide their legacy match, silently
	var legacy int
	for _, m := range scanned.Matches {
		found[site{m.File, m.Line, m.Rule}] = true
	}
	for _, o := range scanned.OptOuts {
		path := o.File
		if !touched.has(path, o.Line) {
			if o.Problem == "" {
				legacyOptOut[site{path, o.Line, o.Rule}] = true
			}
			continue
		}
		d := diag.Diagnostic{File: path, Line: o.Line, Col: o.Col, Code: o.Rule, Sev: diag.Warning, Kind: diag.OptOut}
		s := site{path, o.Line, o.Rule}
		switch {
		case o.Problem != "":
			d.Code = "OPTOUT"
			d.Msg = "malformed opt-out suppresses nothing: " + o.Problem
		case found[s]:
			optedOut[s] = true
			d.Msg = "opt-out: " + o.Reason
		default:
			d.Msg = "opt-out: " + o.Reason + " (suppresses nothing: no " + o.Rule + " finding on this line)"
		}
		ds = append(ds, d)
		optOutCount++
	}
	for _, m := range scanned.Matches {
		path := m.File
		if !touched.has(path, m.Line) {
			if req.Legacy && !legacyOptOut[site{path, m.Line, m.Rule}] {
				ds = append(ds, diag.Diagnostic{File: path, Line: m.Line, Col: m.Col, Code: m.Rule, Sev: diag.Warning, Kind: diag.Legacy, Msg: m.Msg})
				legacy++
			}
			continue
		}
		if optedOut[site{path, m.Line, m.Rule}] {
			continue
		}
		sev := diag.Warning
		if m.Severity == "error" {
			sev = diag.Error
			exit = 1
			errs++
		} else {
			warns++
		}
		ds = append(ds, diag.Diagnostic{File: path, Line: m.Line, Col: m.Col, Code: m.Rule, Sev: sev, Kind: diag.Finding, Msg: m.Msg})
	}
	lines := 0
	for _, f := range files {
		lines += touched.lineCount(f)
	}
	sum := summary(errs, warns, optOutCount, len(files), lines)
	if sc.base != "" {
		sum += "; base " + sc.base + " (" + sc.source + ")"
	}
	if legacy > 0 {
		sum += "; " + count(legacy, "legacy violation") + " listed"
	}
	return render(req, exit, ds, sum), nil
}

// Rules verifies every example of every rule in dirs, none meaning ./.june/rules.
// Exit 0 means every example behaved. Any rule fault is Exit 2.
func Rules(ctx context.Context, env Env, dirs []string) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}
	req := Request{}
	req.Format = formatFor(env, req)
	if req.Format == diag.GitLab {
		req.Format = diag.Text // verify has no findings, and an empty report would stand for a clean check
	}
	rs, refusal, err := load(ctx, env, dirs, req)
	if refusal != nil {
		return *refusal, nil
	}
	if err != nil {
		return Result{}, err
	}
	return render(req, 0, nil, fmt.Sprintf("june: verified %s, %s", count(rs.Rules(), "rule"), count(rs.Examples(), "example"))), nil
}

// load verifies the rules in dirs. A rule fault comes back as a refusal.
// Cancellation comes back as an error, and wins over a fault that cancellation caused.
func load(ctx context.Context, env Env, dirs []string, req Request) (*rules.RuleSet, *Result, error) {
	if len(dirs) == 0 {
		dirs = []string{defaultRules}
		if _, err := os.Stat(resolve(env.Dir, defaultRules)); errors.Is(err, fs.ErrNotExist) {
			res := refuse(req, "no rules to run: june ships no rules. Fix: supply rules with --rules DIR or put them in "+defaultRules)
			return nil, &res, nil
		}
	}
	dirs = distinct(env.Dir, dirs)
	abs := make([]string, len(dirs))
	for i, d := range dirs {
		abs[i] = resolve(env.Dir, d)
	}
	rs, faults := rules.Load(ctx, abs...)
	if err := ctx.Err(); err != nil && len(faults) > 0 {
		return nil, nil, err
	}
	if len(faults) > 0 {
		res := refuse(req, faultMessages(env.Dir, dirs, faults)...)
		return nil, &res, nil
	}
	return rs, nil, nil
}

// distinct drops a directory that an earlier one already names, so the same directory never conflicts with itself.
func distinct(base string, dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if d == "" {
			d = defaultRules
		}
		key := resolve(base, d)
		if real, err := filepath.EvalSymlinks(key); err == nil {
			key = real
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	return out
}

// summary is the closing line of a verdict: finding counts and the scope that was covered.
func summary(errs, warns, optOuts, files, lines int) string {
	return fmt.Sprintf("june: %s, %s, %s; checked %s, %s",
		count(errs, "error"), count(warns, "warning"), count(optOuts, "opt-out"),
		count(files, "file"), count(lines, "changed line"))
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// formatFor is the requested format, else the native one of the CI that env says it runs in.
func formatFor(env Env, req Request) diag.Format {
	if req.Format != "" {
		return req.Format
	}
	switch {
	case onGitHub(env):
		return diag.GitHub
	case gitlab(env):
		return diag.GitLab
	}
	return diag.Text
}

// repoPrefix is where env.Dir sits under the repository root, "" at the root or outside git.
// Only GitHub annotations and SARIF read paths from the root, so no other format asks git.
func repoPrefix(ctx context.Context, env Env, f diag.Format) string {
	if f != diag.GitHub && f != diag.SARIF {
		return ""
	}
	out, err := git(ctx, env, "", "rev-parse", "--show-prefix")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// findTouched picks the touched-line adapter: the supplied list, else git against Base or an inferred base.
// The scope is zero for a supplied list. The touched set is never "every line": what cannot be known is refused.
func findTouched(ctx context.Context, env Env, req Request) (touchedSet, scope, *string, error) {
	if req.Changed == nil {
		if req.Base != "" {
			return gitTouched(ctx, env, req.Base, sourceFlag)
		}
		if base := mergeRequestBase(ctx, env); base != "" {
			return gitTouched(ctx, env, base, sourceMR)
		}
		return gitTouched(ctx, env, "", "")
	}
	var touched touchedSet
	var bad *string
	read := make(chan struct{})
	go func() {
		touched, bad = readTouched(env, req.Changed)
		close(read)
	}()
	select {
	case <-ctx.Done():
		return nil, scope{}, nil, ctx.Err()
	case <-read:
	}
	if err := ctx.Err(); err != nil {
		return nil, scope{}, nil, err
	}
	return touched, scope{}, bad, nil
}

// gitlab reports whether the run is a GitLab CI job.
func gitlab(env Env) bool { return env.Vars["GITLAB_CI"] == "true" }

// mergeRequestBase is the commit a GitLab merge request pipeline judges from, or "" outside one.
// Detached pipelines use the diff base. Merged results and merge trains check out a merge commit,
// so the merge request's own changes are that commit's first parent through HEAD.
// CI_MERGE_REQUEST_TARGET_BRANCH_SHA is never used for this: it is the target branch tip, which on a later
// merge train car sits behind the first parent and would blame this merge request for the cars ahead of it.
// An unresolvable first parent comes back as "HEAD^1", which the git adapter refuses with a named fix.
func mergeRequestBase(ctx context.Context, env Env) string {
	if !gitlab(env) {
		return ""
	}
	switch env.Vars["CI_MERGE_REQUEST_EVENT_TYPE"] {
	case "merged_result", "merge_train":
		if out, err := git(ctx, env, "", "rev-parse", "--verify", "--quiet", "HEAD^1^{commit}"); err == nil {
			if sha := strings.TrimSpace(out); sha != "" {
				return sha
			}
		}
		return "HEAD^1"
	default:
		return env.Vars["CI_MERGE_REQUEST_DIFF_BASE_SHA"]
	}
}

// refuse explains why there is no verdict. A GitLab refusal is text only, since a report would read as a clean run;
// GitHub annotates it as an error.
func refuse(req Request, msgs ...string) Result {
	if req.Format == diag.GitLab {
		req.Format = diag.Text
	}
	ds := make([]diag.Diagnostic, len(msgs))
	for i, m := range msgs {
		ds[i] = diag.Diagnostic{Kind: diag.Failure, Sev: diag.Error, Msg: m}
	}
	return render(req, 2, ds, "")
}

func render(req Request, exit int, ds []diag.Diagnostic, summary string) Result {
	shown := ds
	if req.root != "" {
		shown = make([]diag.Diagnostic, len(ds))
		for i, d := range ds {
			if d.File != "" {
				d.Path = req.root + d.File
			}
			shown[i] = d
		}
	}
	out, files := diag.Render(shown, summary, req.Format)
	if req.stepSummary != "" {
		files = append(files, diag.File{Path: req.stepSummary, Data: diag.StepSummary(shown, summary), Append: true})
	}
	return Result{Exit: exit, Out: out, Files: files, Diags: ds}
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// faultMessages words rule faults as "<cause>. Fix: <instruction>" with a relative rules-directory label.
// display holds the directories as the caller spelled them, indexed like Fault.Dir.
// base is the working directory an absolute spelling is made relative to.
func faultMessages(base string, display []string, faults []rules.Fault) []string {
	msgs := make([]string, len(faults))
	for i, f := range faults {
		cause := f.Cause
		if f.Also != nil {
			cause += " (also defined in " + label(base, display[f.Also.Dir], f.Also.Path) + ")"
		}
		msgs[i] = label(base, display[f.Dir], f.Path) + ": " + cause + ". Fix: " + f.Fix
	}
	return msgs
}

// label names a file under a rules directory without leaking an absolute path.
// An absolute directory is made relative to base, and falls back to its base name when that fails.
func label(base, display, path string) string {
	where := filepath.Clean(display)
	if filepath.IsAbs(where) {
		rel, err := filepath.Rel(base, where)
		if err != nil || filepath.IsAbs(rel) {
			rel = filepath.Base(where)
			if filepath.IsAbs(rel) {
				rel = "."
			}
		}
		where = rel
	}
	if path != "" {
		where = filepath.Join(where, path)
	}
	return filepath.ToSlash(where)
}
