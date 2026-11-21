// Package check decides one change: which lines are touched, which findings land on them, and the exit code.
package check

import (
	"context"
	"errors"
	"io"
	"path/filepath"

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
	// Changed is the supplied touched-line list, one "path:7" or "path:7-12" per line.
	Changed io.Reader
	// Rules is the rules directory; "" means ./.june/rules.
	Rules string
	// Format is the output format; "" means text.
	Format diag.Format
}

// Result is the verdict and its rendering.
type Result struct {
	Exit  int
	Out   []byte
	Files []diag.File
	Diags []diag.Diagnostic
}

// Change judges one change. Exit is 1 if and only if an error-severity match is anchored on a touched line.
// Exit 2 means june could not produce a verdict and Out says why. An error is returned only when
// june cannot even explain the failure.
func Change(ctx context.Context, env Env, req Request) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}

	touched, bad := readTouched(env, req.Changed)
	if bad != nil {
		return refuse(req, *bad), nil
	}

	rulesDir := req.Rules
	if rulesDir == "" {
		rulesDir = defaultRules
	}
	rs, faults := rules.Load(ctx, resolve(env.Dir, rulesDir))
	if len(faults) > 0 {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return refuse(req, faultMessages(rulesDir, faults)...), nil
	}

	files := touched.pythonFiles()
	abs := make([]string, len(files))
	byAbs := make(map[string]string, len(files))
	for i, f := range files {
		abs[i] = filepath.Join(env.Dir, filepath.FromSlash(f))
		byAbs[abs[i]] = f
	}
	scanned, err := rs.Scan(ctx, abs)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return refuse(req, "ast-grep failed while scanning ("+err.Error()+"). Fix: run ast-grep scan on the changed files to see why"), nil
	}

	var ds []diag.Diagnostic
	exit := 0
	for _, m := range scanned.Matches {
		path := byAbs[m.File]
		if !touched.has(path, m.Line) {
			continue
		}
		sev := diag.Warning
		if m.Severity == "error" {
			sev = diag.Error
			exit = 1
		}
		ds = append(ds, diag.Diagnostic{File: path, Line: m.Line, Col: m.Col, Code: m.Rule, Sev: sev, Kind: diag.Finding, Msg: m.Msg})
	}
	return render(req, exit, ds), nil
}

func refuse(req Request, msgs ...string) Result {
	ds := make([]diag.Diagnostic, len(msgs))
	for i, m := range msgs {
		ds[i] = diag.Diagnostic{Kind: diag.Failure, Sev: diag.Error, Msg: m}
	}
	return render(req, 2, ds)
}

func render(req Request, exit int, ds []diag.Diagnostic) Result {
	out, files := diag.Render(ds, "", req.Format)
	return Result{Exit: exit, Out: out, Files: files, Diags: ds}
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// faultMessages words rule faults as "<cause>. Fix: <instruction>", naming files relative to the rules directory as given.
func faultMessages(display string, faults []rules.Fault) []string {
	msgs := make([]string, len(faults))
	for i, f := range faults {
		where := filepath.ToSlash(filepath.Clean(display))
		if f.Path != "" {
			where = filepath.ToSlash(filepath.Join(display, f.Path))
		}
		msgs[i] = where + ": " + f.Cause + ". Fix: " + f.Fix
	}
	return msgs
}
