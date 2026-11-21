package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got, want := out.String(), "june "+version+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestUnknownArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--bogus"}, strings.NewReader(""), &out, &errOut, check.Env{Dir: t.TempDir()}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

const py012 = "PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.\n"

// project builds a temp tree with the shipped rules at .june/rules and one Python file.
func project(t *testing.T) check.Env {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "..", "rules", "python")
	for _, rel := range []string{"PY012.yml", filepath.Join("rule-tests", "PY012-test.yml")} {
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, ".june", "rules", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("x = 1\ndef f(a=[]):\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return check.Env{Dir: dir}
}

func TestChangedLinesFileFindingExitsOne(t *testing.T) {
	env := project(t)
	list := filepath.Join(t.TempDir(), "changed.txt")
	if err := os.WriteFile(list, []byte("app.py:2-3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", list}, strings.NewReader(""), &out, &errOut, env)
	if code != 1 || out.String() != "app.py:2:9: "+py012 || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestChangedLinesStdinUntouchedExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:1\n"), &out, &errOut, project(t))
	if code != 0 || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
}

func TestRulesFlag(t *testing.T) {
	env := project(t)
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--rules", "nowhere", "--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, env)
	want := "june: error: nowhere: the rules directory does not exist. Fix: create it or point --rules at an existing rules directory\n"
	if code != 2 || out.String() != want {
		t.Errorf("exit = %d, stdout = %q, want 2 and %q", code, out.String(), want)
	}
}

func TestBadListExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py\n"), &out, &errOut, project(t))
	if code != 2 || !strings.HasPrefix(out.String(), "june: error: changed-lines line 1") || !strings.Contains(out.String(), ". Fix: ") {
		t.Errorf("exit = %d, stdout = %q", code, out.String())
	}
}

func TestMissingListFileExitsTwo(t *testing.T) {
	for _, file := range []string{"absent.txt", filepath.Join(t.TempDir(), "absent.txt")} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"--changed-lines", file}, strings.NewReader(""), &out, &errOut, project(t))
		want := "june: error: the changed-lines file cannot be read. Fix: pass an existing file to --changed-lines\n"
		if code != 2 || errOut.String() != want || out.Len() != 0 {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
		}
	}
}

func TestInterruptedRunExplainsItself(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, project(t))
	want := "june: error: june was interrupted before it reached a verdict. Fix: run june again and let it finish\n"
	if code != 2 || errOut.String() != want || out.Len() != 0 {
		t.Errorf("exit = %d, stderr = %q, stdout = %q, want 2 and %q", code, errOut.String(), out.String(), want)
	}
}

func TestMissingWorkingDirectoryExplainsItself(t *testing.T) {
	getwd := func() (string, error) { return "", errors.New("getwd: no such file or directory") }
	var out, errOut bytes.Buffer
	code := start(context.Background(), []string{"--changed-lines", "-"}, strings.NewReader("app.py:2\n"), &out, &errOut, getwd)
	want := "june: error: the working directory cannot be determined (getwd: no such file or directory). Fix: run june from an existing directory\n"
	if code != 2 || out.Len() != 0 || errOut.String() != want {
		t.Errorf("exit = %d, stderr = %q, stdout = %q, want 2 and %q", code, errOut.String(), out.String(), want)
	}
}
