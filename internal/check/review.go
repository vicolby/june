package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/vicolby/june/internal/diag"
	"github.com/vicolby/june/internal/review"
	"github.com/vicolby/june/internal/rules"
)

// reviewTimeout bounds the whole review run. Calls that are not made when it ends are reported as not reviewed.
const reviewTimeout = 10 * time.Minute

// reviewNames are the names june review gives the files it writes, so that it never overwrites the check's.
var reviewNames = diag.Names{Heading: "june review", GitLabReport: diag.ReviewGitLabReport}

// ReviewRequest describes one review of a change by a model.
type ReviewRequest struct {
	// Base, Changed, Format, Exclude, Paths and IgnoreSpace mean what they mean in Request, and pick the same lines.
	Base        string
	Changed     io.Reader
	Format      diag.Format
	Exclude     []string
	Paths       []string
	IgnoreSpace bool
	// AI says what to review against and how to ask. Its paths are relative to the working directory.
	AI review.Config
	// Model answers the calls. The caller builds it, so this package never reaches a network.
	Model review.Model
}

// Review asks a model whether the lines a change touched break the rules in req.AI.Rules.
// It runs only in GitHub Actions or GitLab CI. Every finding is a warning, so Exit is 0 whatever the model says
// and whether or not a call failed; a call that failed is a warning in the output and in the summary.
// Exit 2 means misuse or no way to know what to review: not CI, bad configuration, or no touched lines to be found.
// An empty Env.Dir or cancellation returns an error.
func Review(ctx context.Context, env Env, req ReviewRequest) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	base := Request{Base: req.Base, Changed: req.Changed, Format: req.Format, Exclude: req.Exclude, Paths: req.Paths, IgnoreSpace: req.IgnoreSpace, names: reviewNames}
	early := base
	early.Format = formatFor(env, base)
	early.stepSummary = env.Vars["GITHUB_STEP_SUMMARY"]
	if msg := reviewGuard(env); msg != "" {
		return refuse(early, msg), nil
	}
	plan, faults := review.Prepare(env.Dir, req.AI)
	if len(faults) > 0 {
		return refuse(early, faults...), nil
	}

	scp, refusal, err := scopeChange(ctx, env, base)
	defer scp.close()
	if err != nil {
		return Result{}, err
	}
	if refusal != nil {
		return *refusal, nil
	}
	return reviewScoped(ctx, env, scp, plan, req.Model)
}

// reviewGuard is the refusal to run outside CI, or "" when the run is allowed.
// The code of a pull request goes to a model with the repository's key, so it runs only where a key is a CI secret
// and a fork's pull request has none.
func reviewGuard(env Env) string {
	switch {
	case env.Vars["PRE_COMMIT"] == "1":
		return "june review does not run as a pre-commit hook. Fix: run it in a GitHub Actions or GitLab CI job, with the API key in a CI secret"
	case !onGitHub(env) && !gitlab(env):
		return "june review runs only in GitHub Actions or GitLab CI in this release, and neither was detected. Fix: run it in a CI job, with the API key in a CI secret"
	case onGitHub(env) && env.Vars["GITHUB_EVENT_NAME"] == "pull_request_target":
		return "june review would send the code of a pull request to the API with this repository's key under pull_request_target. Fix: run it on pull_request, which pull requests from forks run without secrets"
	}
	return ""
}

func reviewScoped(ctx context.Context, env Env, scp scoped, plan *review.Plan, model review.Model) (Result, error) {
	req, touched, sc := scp.req, scp.touched, scp.sc
	root := scp.root(env)
	files, excluded := scp.skip.filter(touched.pythonFiles())

	var ds []diag.Diagnostic
	var sources []review.Source
	skipped := 0
	warns, lines := 0, 0
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			return refuse(req, f+" cannot be read ("+readReason(err)+"). Fix: check that the checkout holds the files of the change"), nil
		}
		text, reason := rules.Decode(raw)
		if reason != "" {
			skipped++
			warns++
			ds = append(ds, encodingWarning(f, reason))
			continue
		}
		src := review.Source{Path: f, Text: string(text)}
		for _, s := range touched[f] {
			src.Touched = append(src.Touched, review.Span{From: s.from, To: s.to})
		}
		sources = append(sources, src)
		lines += touched.lineCount(f)
	}

	rctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	out, err := plan.Run(rctx, model, sources)
	if err != nil {
		return Result{}, err
	}
	for _, d := range out.Diags {
		if d.Sev == diag.Warning {
			warns++
		}
	}
	ds = append(ds, out.Diags...)

	if lines == 0 {
		refusal, warning, err := scp.judgedNothing(ctx, env)
		if err != nil {
			return Result{}, err
		}
		if refusal != nil {
			return *refusal, nil
		}
		if warning != nil {
			warns++
			ds = append(ds, *warning)
		}
	}
	sum := summary(0, warns, 0, len(files)-skipped, lines)
	if skipped > 0 {
		sum += "; " + count(skipped, "file") + " skipped, not readable as text"
	}
	sum += scopeNotes(sc, len(files), excluded) + "; " + out.Detail
	return render(req, 0, ds, sum), nil
}

func readReason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return fmt.Sprint(err)
}
