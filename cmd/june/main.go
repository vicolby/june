package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// issuesURL is where a june bug is reported.
const issuesURL = "https://github.com/vicolby/june/issues"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	os.Exit(start(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getwd))
}

// start snapshots the environment and runs june.
func start(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getwd func() (string, error)) int {
	env, err := snapshotEnv(getwd)
	if err != nil {
		return fail(stderr, fmt.Sprintf("the working directory cannot be determined (%v). Fix: run june from an existing directory", err))
	}
	return run(ctx, args, stdin, stdout, stderr, env)
}

// snapshotEnv captures the caller's working directory and environment for check.
func snapshotEnv(getwd func() (string, error)) (check.Env, error) {
	dir, err := getwd()
	if err != nil {
		return check.Env{}, err
	}
	vars := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	return check.Env{Dir: dir, Vars: vars}, nil
}

// usage is the help text: `june --help` and `june -h` print it to stdout and exit 0.
const usage = `usage:
  june [--format FORMAT] [--legacy] [--ignore-whitespace] [--rules DIR]... [--exclude GLOB]... [--base REF | --changed-lines FILE] [PATH]...
  june init [--ci github|gitlab]
  june verify [--rules DIR]... [--rule ID] [--format FORMAT]
  june new [--rules DIR] RULE
  june --version
  june --help | -h

PATH narrows the check to those files and directories, relative to the working directory.
.june/rules and .june/config.yml are found at the repository root, or in the working directory outside git.
Flags must precede paths.

flags:
  --base REF            judge lines changed since the merge base of REF and HEAD
  --changed-lines FILE  file of touched lines, path:7 or path:7-12 per line; - reads stdin
  --exclude GLOB        keep paths out of the check, repeatable; adds to exclude in .june/config.yml
  --format FORMAT       text, json, sarif, github or gitlab (default: github under GitHub Actions, gitlab under GitLab CI, else text); verify takes it too
  --rule ID             with verify: check the examples of one rule only
  --ignore-whitespace   do not count a line whose only change is whitespace as touched (git diff -w)
  --legacy              also list violations on untouched lines, information only
  --rules DIR           rules directory, repeatable (default: rules in .june/config.yml, else .june/rules); june new takes one
  --ci PROVIDER         with init: write github (default) or gitlab CI configuration
  --version             print the june and ast-grep versions
  --help, -h            print this help

exit codes:
  0  no error-severity finding on a touched line
  1  an error-severity finding on a touched line with no valid opt-out
  2  no verdict: bad usage, bad rules directory, shallow clone, no base, missing ast-grep
`

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, env check.Env) int {
	if len(args) > 0 && args[0] == "verify" {
		return verify(ctx, args[1:], stdout, stderr, env)
	}
	if len(args) > 0 && args[0] == "init" {
		return initProject(ctx, args[1:], stdout, stderr, env)
	}
	if len(args) > 0 && args[0] == "new" {
		return newRule(ctx, args[1:], stdout, stderr, env)
	}
	fs := flag.NewFlagSet("june", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "print the version")
	base := fs.String("base", "", "git ref; judge lines changed since its merge base with HEAD")
	changedLines := fs.String("changed-lines", "", "file of touched lines, path:7 or path:7-12 per line; - reads stdin")
	format := fs.String("format", "", "output format: text, json, sarif, github or gitlab")
	ignoreSpace := fs.Bool("ignore-whitespace", false, "do not count a line whose only change is whitespace as touched (git diff -w)")
	legacy := fs.Bool("legacy", false, "also list violations on untouched lines, information only")
	var rulesDirs, excludes dirList
	fs.Var(&rulesDirs, "rules", "rules directory, repeatable (default ./.june/rules)")
	fs.Var(&excludes, "exclude", "path glob to keep out of the check, repeatable")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		return usageError(stderr, err.Error())
	}
	paths := fs.Args()
	for _, arg := range paths {
		if strings.HasPrefix(arg, "-") {
			return usageError(stderr, "flags must precede paths. Use: june [flags] [paths]; prefix paths starting with - with ./")
		}
	}
	if *showVersion {
		line := check.ASTGrep(ctx)
		if err := ctx.Err(); err != nil {
			return finish(env.Dir, stdout, stderr, check.Result{}, err)
		}
		fmt.Fprintf(stdout, "june %s\n%s\n", version, line)
		return 0
	}

	req := check.Request{Rules: rulesDirs, Exclude: excludes, Paths: paths, Base: *base, Legacy: *legacy, IgnoreSpace: *ignoreSpace}
	if *format != "" {
		f, ok := diag.ParseFormat(*format)
		if !ok {
			return usageError(stderr, fmt.Sprintf("unknown format %q", *format))
		}
		req.Format = f
	}
	switch *changedLines {
	case "":
	case "-":
		req.Changed = stdin
	default:
		f, err := os.Open(*changedLines)
		if err != nil {
			return fail(stderr, "the changed-lines file cannot be read. Fix: pass an existing file to --changed-lines")
		}
		defer func() { _ = f.Close() }() // Read-only file; closing cannot change the verdict.
		req.Changed = f
	}

	res, err := check.Change(ctx, env, req)
	return finish(env.Dir, stdout, stderr, res, err)
}

// initProject is `june init [--ci github|gitlab]`: it writes the rules directory with a starter rule, a config, and CI and hook configuration.
func initProject(ctx context.Context, args []string, stdout, stderr io.Writer, env check.Env) int {
	fs := flag.NewFlagSet("june init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ci := fs.String("ci", "github", "CI provider: github or gitlab")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		return usageError(stderr, err.Error())
	}
	if fs.NArg() > 0 {
		return usageError(stderr, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	res, err := check.Init(ctx, env, check.InitRequest{CI: *ci, Version: version})
	return finish(env.Dir, stdout, stderr, res, err)
}

// verify is `june verify [--rules DIR]... [--rule ID] [--format FORMAT]`: every example of every rule, or of one rule, behaves, or the run is refused.
func verify(ctx context.Context, args []string, stdout, stderr io.Writer, env check.Env) int {
	fs := flag.NewFlagSet("june verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var rulesDirs dirList
	fs.Var(&rulesDirs, "rules", "rules directory, repeatable (default ./.june/rules)")
	rule := fs.String("rule", "", "verify the examples of this rule id only")
	format := fs.String("format", "", "output format: text, json, sarif, github or gitlab")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		return usageError(stderr, err.Error())
	}
	if fs.NArg() > 0 {
		return usageError(stderr, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	req := check.VerifyRequest{Rules: rulesDirs, Rule: *rule}
	if *format != "" {
		f, ok := diag.ParseFormat(*format)
		if !ok {
			return usageError(stderr, fmt.Sprintf("unknown format %q", *format))
		}
		req.Format = f
	}
	res, err := check.Verify(ctx, env, req)
	return finish(env.Dir, stdout, stderr, res, err)
}

// newRule is `june new [--rules DIR] RULE`: it writes a minimal rule and its examples, which verify accepts as written.
func newRule(ctx context.Context, args []string, stdout, stderr io.Writer, env check.Env) int {
	fs := flag.NewFlagSet("june new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var rulesDirs dirList
	fs.Var(&rulesDirs, "rules", "rules directory (default ./.june/rules)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		return usageError(stderr, err.Error())
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "june new needs exactly one rule id after its flags")
	}
	res, err := check.New(ctx, env, rulesDirs, fs.Arg(0))
	return finish(env.Dir, stdout, stderr, res, err)
}

// dirList is a repeatable directory flag that keeps the order given.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }

func (d *dirList) Set(v string) error {
	*d = append(*d, v)
	return nil
}

// finish writes a verdict and its extra files under dir, or explains why there is none, and returns the exit code.
func finish(dir string, stdout, stderr io.Writer, res check.Result, err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fail(stderr, "june was interrupted before it reached a verdict. Fix: run june again and let it finish")
	}
	if err != nil {
		return fail(stderr, fmt.Sprintf("%v. Fix: report this as a june bug at %s with june %s and the command you ran", err, issuesURL, version))
	}
	_, _ = stdout.Write(res.Out) // Output write failures have no recovery path, as with fail.
	for _, f := range res.Files {
		if f.Append {
			// The job summary is a convenience beside the verdict, so a failure to write it never changes the exit code.
			if err := appendFile(f.Path, f.Data); err != nil {
				fmt.Fprintf(stderr, "june: warning: the job summary %s cannot be written (%v)\n", f.Path, err)
			}
			continue
		}
		// A report that cannot be written is no verdict for the CI that reads it, so it exits 2, whatever the findings were.
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(f.Path)), f.Data, 0o644); err != nil {
			return fail(stderr, fmt.Sprintf("the report %s cannot be written (%v). Fix: make the working directory writable", f.Path, err))
		}
	}
	return res.Exit
}

func appendFile(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	_, err = f.Write(data)
	return err
}

func usageError(stderr io.Writer, cause string) int {
	return fail(stderr, cause+". Fix: usage is june [--format FORMAT] [--legacy] [--ignore-whitespace] [--rules DIR]... [--exclude GLOB]... [--base REF | --changed-lines FILE] [PATH]..., june init [--ci github|gitlab], june verify [--rules DIR]... [--rule ID] [--format FORMAT], june new [--rules DIR] RULE, june --version, or june --help")
}

func fail(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "june: error: %s\n", msg)
	return 2
}
