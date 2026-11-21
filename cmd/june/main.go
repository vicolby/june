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
	fs := flag.NewFlagSet("june", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "print the version")
	changedLines := fs.String("changed-lines", "", "file of touched lines, path:7 or path:7-12 per line; - reads stdin")
	rulesDir := fs.String("rules", "", "rules directory (default ./.june/rules)")
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

	req := check.Request{Rules: *rulesDir}
	switch *changedLines {
	case "":
	case "-":
		req.Changed = stdin
	default:
		f, err := os.Open(*changedLines)
		if err != nil {
			return fail(stderr, "the changed-lines file cannot be read. Fix: pass an existing file to --changed-lines")
		}
		defer f.Close()
		req.Changed = f
	}

	res, err := check.Change(ctx, env, req)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fail(stderr, "june was interrupted before it reached a verdict. Fix: run june again and let it finish")
	}
	if err != nil {
		return fail(stderr, fmt.Sprintf("%v. Fix: report this as a june bug", err))
	}
	stdout.Write(res.Out)
	return res.Exit
}

func usageError(stderr io.Writer, cause string) int {
	return fail(stderr, cause+". Fix: usage is june [--rules DIR] --changed-lines FILE, or june --version")
}

func fail(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "june: error: %s\n", msg)
	return 2
}
