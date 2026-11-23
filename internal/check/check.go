// Package check decides one change: which lines are touched, which findings land on them, and the exit code.
package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// Rules lists the rules directories, merged into one set; empty means the rules of .june/config.yml, else .june/rules.
	// Default directories resolve from the repository root, or the working directory outside git.
	// A directory named twice counts once, and a rule id defined in two files is a rule fault.
	Rules []string
	// Format is the output format, one of diag's; empty is inferred: GitHub under GitHub Actions, GitLab under GitLab CI, else text.
	Format diag.Format
	// Exclude lists path globs, relative to the working directory, that keep files out of the check.
	// They add to the exclude list of .june/config.yml.
	Exclude []string
	// Paths narrows the check to these files and directories, relative to the working directory; empty means everything.
	// A path that does not exist, or lies outside the working directory, is a refusal.
	Paths []string
	// Legacy also lists rule matches on untouched lines of the checked files, as information only.
	// They never change Exit; off by default.
	Legacy bool
	// IgnoreSpace treats a line whose only change is whitespace (git diff -w semantics) as untouched.
	// It applies to a git-derived change and is off by default: a touched line that violates a rule always fails.
	// It is meaningless with Changed, whose lines the caller already chose, so Change refuses the pair.
	IgnoreSpace bool

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
	proj, bad := findProject(ctx, env)
	if bad != nil {
		return refuse(req, *bad), nil
	}
	skip, bad := excludes(env, proj, req)
	if bad != nil {
		return refuse(req, *bad), nil
	}
	touched, sc, bad, err := findTouched(ctx, env, req, proj.cfg.Base, skip)
	if sc.scanDir != "" {
		defer func() { _ = os.RemoveAll(sc.scanDir) }()
	}
	if err != nil {
		return Result{}, err
	}
	if bad != nil {
		return refuse(req, *bad), nil
	}

	rs, refusal, err := load(ctx, env, proj, req.Rules, "", req)
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
	files, excluded := skip.filter(touched.pythonFiles())
	scanned, err := rs.Scan(ctx, root, files)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return refuse(req, "ast-grep failed while scanning ("+err.Error()+"). Fix: run ast-grep scan on the changed files to see why"), nil
	}

	var ds []diag.Diagnostic
	// A file that cannot be read as text is left out loudly: a warning naming it, and a count in the summary.
	// Scan reports the slash-separated path it was given, already relative to the working directory.
	skipped := map[string]bool{}
	var errs, warns, optOutCount int
	for _, s := range scanned.Skipped {
		skipped[s.File] = true
		warns++
		ds = append(ds, diag.Diagnostic{File: s.File, Line: 1, Col: 1, Code: "ENCODING", Sev: diag.Warning, Kind: diag.Finding,
			Msg: "file not scanned: " + s.Reason + ". Fix: add a PEP 263 coding declaration for a supported encoding, or save it as UTF-8"})
	}
	exit := 0
	type site struct {
		file string
		line int
		rule string
	}
	optedOut := map[site]bool{}
	found := map[site]int{}         // matches per line and rule: an opt-out covers a line with exactly one
	legacyOptOut := map[site]bool{} // valid opt-outs on untouched lines hide their legacy match, silently
	var legacy int
	for _, m := range scanned.Matches {
		found[site{m.File, m.Line, m.Rule}]++
	}
	for _, o := range scanned.OptOuts {
		path := o.File
		if !touched.has(path, o.Line) {
			if o.Problem == "" && found[site{path, o.Line, o.Rule}] == 1 {
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
		case found[s] == 1:
			optedOut[s] = true
			d.Msg = "opt-out: " + o.Reason
		case found[s] > 1:
			d.Msg = fmt.Sprintf("opt-out: %s (suppresses nothing: %d %s findings on this line, an opt-out covers exactly one. Fix: fix a violation or split the line)", o.Reason, found[s], o.Rule)
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
		ds = append(ds, diag.Diagnostic{File: path, Line: m.Line, Col: m.Col, Code: m.Rule, Sev: sev, Kind: diag.Finding, Msg: m.Msg, Note: m.Note, URL: m.URL})
	}
	// A touched line inside a syntax error was judged on whatever the parser recovered, so say so once per file.
	warned := map[string]bool{}
	for _, pe := range scanned.ParseErrors {
		line, ok := touched.firstIn(pe.File, pe.From, pe.To)
		if !ok || warned[pe.File] {
			continue
		}
		warned[pe.File] = true
		warns++
		ds = append(ds, diag.Diagnostic{File: pe.File, Line: line, Col: 1, Code: "PARSE", Sev: diag.Warning, Kind: diag.Finding,
			Msg: "a touched line is inside a syntax error, so rules may have checked less than they appear to. Fix: fix the syntax error and run june again"})
	}
	lines := 0
	checked := 0
	for _, f := range files {
		if !skipped[f] {
			checked++
			lines += touched.lineCount(f)
		}
	}
	if lines == 0 && pullRequestEvent(env) {
		base := sc.base
		if base == "" || sc.source == sourceRefs || sc.source == sourceStaged {
			// Supplied lines and pre-commit scopes do not identify the PR base.
			base, _, _ = eventBase(env)
		}
		if base != "" {
			head, herr := git(ctx, env, "", "rev-parse", "HEAD^{commit}")
			target, terr := git(ctx, env, "", "rev-parse", base+"^{commit}")
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if herr == nil && terr == nil && strings.TrimSpace(head) == strings.TrimSpace(target) {
				return refuse(req, fmt.Sprintf("HEAD is the base %q of this pull request, so nothing was judged. Fix: check out the change under review (with pull_request_target, set ref on actions/checkout to the pull request head), or run june on pull_request", base)), nil
			}
		}
		warns++
		ds = append(ds, diag.Diagnostic{Kind: diag.Failure, Sev: diag.Warning,
			Msg: "this pull request judged 0 changed lines. Fix: if it changes Python files, check that the checkout is the change under review (pull_request_target checks out the base branch by default)"})
	}
	sum := summary(errs, warns, optOutCount, checked, lines)
	if n := len(scanned.Skipped); n > 0 {
		sum += "; " + count(n, "file") + " skipped, not readable as text"
	}
	if sc.notRegular > 0 {
		sum += "; " + count(sc.notRegular, "changed .py path") + " skipped, not regular files (symlink or submodule)"
	}
	if sc.notPresent > 0 {
		sum += "; " + count(sc.notPresent, "changed .py path") + " skipped, absent from sparse checkout"
	}
	if len(files) == 0 {
		sum += "; note: no changed Python files found - june reads .py files only, not .pyi or scripts without an extension - so if some were expected, check --base and the checkout"
	}
	if sc.base != "" {
		sum += "; base " + sc.base + " (" + sc.source + ")"
	}
	if excluded > 0 {
		sum += "; " + count(excluded, "file") + " excluded"
	}
	if legacy > 0 {
		sum += "; " + count(legacy, "legacy violation") + " listed"
	}
	return render(req, exit, ds, sum), nil
}

// VerifyRequest describes one verification of rule examples.
type VerifyRequest struct {
	// Rules lists the rules directories; none uses the config, else repository-root .june/rules.
	Rules []string
	// Rule is the id of the one rule to verify, empty for all of them.
	Rule string
	// Format is the output format, as for Request. Verify has no findings, so GitLab renders as text.
	Format diag.Format
}

// Rules verifies every example of every rule in dirs; none uses the config, else repository-root .june/rules.
// Exit 0 means every example behaved. Any rule fault is Exit 2.
func Rules(ctx context.Context, env Env, dirs []string) (Result, error) {
	return Verify(ctx, env, VerifyRequest{Rules: dirs})
}

// Verify is Rules with a choice of one rule and of output format. A failing example is a failure that names the
// examples file and the rule, so a machine-readable format tells a rule author what to fix. A rule id that no rule has is a fault.
func Verify(ctx context.Context, env Env, v VerifyRequest) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}
	req := Request{Format: v.Format}
	req.Format = formatFor(env, req)
	if req.Format == diag.GitLab {
		req.Format = diag.Text // verify has no findings, and an empty report would stand for a clean check
	}
	proj, bad := findProject(ctx, env)
	if bad != nil {
		return refuse(req, *bad), nil
	}
	rs, refusal, err := load(ctx, env, proj, v.Rules, v.Rule, req)
	if refusal != nil {
		return *refusal, nil
	}
	if err != nil {
		return Result{}, err
	}
	return render(req, 0, nil, fmt.Sprintf("june: verified %s, %s with ast-grep %s", count(rs.Rules(), "rule"), count(rs.Examples(), "example"), rs.Version())), nil
}

// New writes a minimal rule and its examples for id into the one rules directory, which verify accepts as written.
// An existing rule is never overwritten. Exit 2 reports invalid input or a file-creation failure.
func New(ctx context.Context, env Env, dirs []string, id string) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}
	req := Request{Format: diag.Text}
	proj, bad := findProject(ctx, env)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if bad != nil {
		return refuse(req, *bad), nil
	}
	if len(dirs) == 0 {
		dirs = proj.rulesDirs(env)
	}
	dirs = distinct(env.Dir, dirs)
	if len(dirs) > 1 {
		return refuse(req, "june new writes into one rules directory, and "+strconv.Itoa(len(dirs))+" were given. Fix: pass --rules once"), nil
	}
	dir := dirs[0]
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	made, fault := rules.Scaffold(resolve(env.Dir, dir), id)
	if fault != nil {
		return refuseWith(req, faultDiagnostics(env.Dir, []string{dir}, []rules.Fault{*fault})...), nil
	}
	var b strings.Builder
	for _, m := range made {
		fmt.Fprintf(&b, "created %s\n", label(env.Dir, dir, m))
	}
	fmt.Fprintf(&b, "next: edit the rule and its examples, then run june verify --rules %s --rule %s\n", shellQuote(dir), id)
	return Result{Out: []byte(b.String())}, nil
}

// ASTGrep describes the ast-grep on PATH for june --version: its version, and a note when june would refuse it.
func ASTGrep(ctx context.Context) string {
	bin, err := exec.LookPath("ast-grep")
	if err != nil {
		return "ast-grep not found on PATH"
	}
	v, err := rules.Version(ctx, bin)
	if err != nil {
		return "ast-grep version unreadable (" + err.Error() + ")"
	}
	if !rules.Supported(v) {
		return fmt.Sprintf("ast-grep %s (unsupported: june needs %s or newer)", v, rules.MinVersion)
	}
	return "ast-grep " + v
}

// load verifies the rules in dirs, or the examples of only the one rule with that id when it is not empty.
// A rule fault comes back as a refusal.
// Cancellation comes back as an error, and wins over a fault that cancellation caused.
func load(ctx context.Context, env Env, proj project, dirs []string, only string, req Request) (*rules.RuleSet, *Result, error) {
	if len(dirs) == 0 {
		dirs = proj.rulesDirs(env)
		if len(proj.cfg.Rules) == 0 {
			if _, err := os.Stat(resolve(env.Dir, dirs[0])); errors.Is(err, fs.ErrNotExist) {
				res := refuse(req, "no rules to run: june ships no rules. Fix: supply rules with --rules DIR, list them under rules in "+defaultConfig+", or put them in "+defaultRules)
				return nil, &res, nil
			}
		}
	}
	dirs = distinct(env.Dir, dirs)
	abs := make([]string, len(dirs))
	for i, d := range dirs {
		abs[i] = resolve(env.Dir, d)
	}
	var rs *rules.RuleSet
	var faults []rules.Fault
	if only != "" {
		rs, faults = rules.LoadRule(ctx, only, abs...)
	} else {
		rs, faults = rules.Load(ctx, abs...)
	}
	if err := ctx.Err(); err != nil && len(faults) > 0 {
		return nil, nil, err
	}
	if len(faults) > 0 {
		res := refuseWith(req, faultDiagnostics(env.Dir, dirs, faults)...)
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
func findTouched(ctx context.Context, env Env, req Request, configBase string, skip *excluder) (touchedSet, scope, *string, error) {
	if req.Changed == nil {
		if req.Base != "" {
			return gitTouched(ctx, env, req.Base, sourceFlag, req.IgnoreSpace, skip)
		}
		if base := mergeRequestBase(ctx, env); base != "" {
			return gitTouched(ctx, env, base, sourceMR, req.IgnoreSpace, skip)
		}
		if configBase != "" && env.Vars["PRE_COMMIT"] != "1" {
			return gitTouched(ctx, env, configBase, sourceConfig, req.IgnoreSpace, skip)
		}
		return gitTouched(ctx, env, "", "", req.IgnoreSpace, skip)
	}
	if req.IgnoreSpace {
		msg := "--ignore-whitespace has no effect on --changed-lines, which already names the touched lines. Fix: drop --ignore-whitespace, or drop --changed-lines to let june read git"
		return nil, scope{}, &msg, nil
	}
	var touched touchedSet
	var bad *string
	read := make(chan struct{})
	go func() {
		touched, bad = readTouched(env, req.Changed, skip)
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
	ds := make([]diag.Diagnostic, len(msgs))
	for i, m := range msgs {
		ds[i] = diag.Diagnostic{Kind: diag.Failure, Sev: diag.Error, Msg: m}
	}
	return refuseWith(req, ds...)
}

// refuseWith is refuse for failures that already carry the file and rule they concern.
func refuseWith(req Request, ds ...diag.Diagnostic) Result {
	if req.Format == diag.GitLab {
		req.Format = diag.Text
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

// faultDiagnostics words rule faults as failures reading "<cause>. Fix: <instruction>" with a rules-directory label.
// A fault that concerns no directory, such as a missing ast-grep, has no label.
// A failure carries the labelled file and the rule id for machine-readable formats.
// display holds the directories as the caller spelled them, indexed like Fault.Dir.
// base is the working directory used to shorten absolute paths within it.
func faultDiagnostics(base string, display []string, faults []rules.Fault) []diag.Diagnostic {
	ds := make([]diag.Diagnostic, len(faults))
	for i, f := range faults {
		cause := f.Cause
		if f.Also != nil {
			cause += " (also defined in " + label(base, display[f.Also.Dir], f.Also.Path) + ")"
		}
		d := diag.Diagnostic{Kind: diag.Failure, Sev: diag.Error, Code: f.Rule}
		if f.Dir == rules.NoDir {
			d.Msg = cause + ". Fix: " + f.Fix
		} else {
			d.File = label(base, display[f.Dir], f.Path)
			d.Msg = d.File + ": " + cause + ". Fix: " + f.Fix
		}
		ds[i] = d
	}
	return ds
}

// label names a file under a rules directory the way the caller spelled it.
// An absolute directory below base is made relative to it. One outside base stays absolute, since a path
// that climbs out of base with ".." only names a place relative to a directory the reader may not be in.
func label(base, display, path string) string {
	where := filepath.Clean(display)
	if filepath.IsAbs(where) {
		if rel, err := filepath.Rel(base, where); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			where = rel
		}
	}
	if path != "" {
		where = filepath.Join(where, path)
	}
	return filepath.ToSlash(where)
}
