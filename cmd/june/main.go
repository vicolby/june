package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/vicolby/june/internal/check"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, env check.Env) int {
	if len(args) > 0 && args[0] == "verify" {
		return verify(ctx, args[1:], stdout, stderr, env)
	}
	fs := flag.NewFlagSet("june", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "print the version")
	changedLines := fs.String("changed-lines", "", "file of touched lines, path:7 or path:7-12 per line; - reads stdin")
	var rulesDirs dirList
	fs.Var(&rulesDirs, "rules", "rules directory, repeatable (default ./.june/rules)")
	if err := fs.Parse(args); err != nil {
		return usageError(stderr, err.Error())
	}
	if fs.NArg() > 0 {
		return usageError(stderr, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if *showVersion {
		fmt.Fprintf(stdout, "june %s\n", version)
		return 0
	}

	req := check.Request{Rules: rulesDirs}
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
	return finish(stdout, stderr, res, err)
}

// verify is `june verify [--rules DIR]...`: every example of every rule behaves, or the run is refused.
func verify(ctx context.Context, args []string, stdout, stderr io.Writer, env check.Env) int {
	fs := flag.NewFlagSet("june verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var rulesDirs dirList
	fs.Var(&rulesDirs, "rules", "rules directory, repeatable (default ./.june/rules)")
	if err := fs.Parse(args); err != nil {
		return usageError(stderr, err.Error())
	}
	if fs.NArg() > 0 {
		return usageError(stderr, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	res, err := check.Rules(ctx, env, rulesDirs)
	return finish(stdout, stderr, res, err)
}

// dirList is a repeatable directory flag that keeps the order given.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }

func (d *dirList) Set(v string) error {
	*d = append(*d, v)
	return nil
}

// finish writes a verdict, or explains why there is none, and returns the exit code.
func finish(stdout, stderr io.Writer, res check.Result, err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fail(stderr, "june was interrupted before it reached a verdict. Fix: run june again and let it finish")
	}
	if err != nil {
		return fail(stderr, fmt.Sprintf("%v. Fix: report this as a june bug", err))
	}
	_, _ = stdout.Write(res.Out) // Output write failures have no recovery path, as with fail.
	return res.Exit
}

func usageError(stderr io.Writer, cause string) int {
	return fail(stderr, cause+". Fix: usage is june [--rules DIR]... --changed-lines FILE, june verify [--rules DIR]..., or june --version")
}

func fail(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "june: error: %s\n", msg)
	return 2
}
