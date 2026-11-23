package check_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

// twoPackages is a repo whose rules sit at the root and whose changes sit in two packages.
func twoPackages(t *testing.T, config string) check.Env {
	t.Helper()
	files := map[string]string{"svc/a.py": "x = 1\n", "lib/b.py": "y = 1\n"}
	if config != "" {
		files[".june/config.yml"] = config
	}
	env := repo(t, files)
	write(t, filepath.Join(env.Dir, "svc/a.py"), "x = 1\n"+old)
	write(t, filepath.Join(env.Dir, "lib/b.py"), "y = 1\n"+old)
	return env
}

func runJune(t *testing.T, env check.Env, req check.Request) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDefaultsResolveFromTheRepositoryRoot(t *testing.T) {
	env := twoPackages(t, "")
	sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
	expect(t, runJune(t, sub, check.Request{Base: "main"}), 1, "a.py:2:9: "+py012)
	if v, err := check.Verify(context.Background(), sub, check.VerifyRequest{}); err != nil || v.Exit != 0 {
		t.Errorf("verify from a subdirectory = %d %q, %v", v.Exit, v.Out, err)
	}
}

func TestWorkingDirectoryJuneDirectoryWinsOverTheRoot(t *testing.T) {
	env := twoPackages(t, "")
	write(t, filepath.Join(env.Dir, "svc/.june/rules/none.txt"), "")
	sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
	res := runJune(t, sub, check.Request{Base: "main"})
	if res.Exit != 2 || !strings.Contains(string(res.Out), "june: error: ") {
		t.Errorf("Exit = %d, Out = %q, want the local .june to be used and refused as no rules", res.Exit, res.Out)
	}
}

func TestPathsNarrowTheCheck(t *testing.T) {
	env := twoPackages(t, "")
	both := "lib/b.py:2:9: " + py012 + "svc/a.py:2:9: " + py012
	tests := []struct {
		name  string
		paths []string
		want  string
		files string
	}{
		{"none means everything", nil, both, "2 files"},
		{"a directory", []string{"svc"}, "svc/a.py:2:9: " + py012, "1 file"},
		{"a file", []string{"lib/b.py"}, "lib/b.py:2:9: " + py012, "1 file"},
		{"dot is everything", []string{"."}, both, "2 files"},
		{"two paths", []string{"svc", "lib/"}, both, "2 files"},
		{"an absolute path under the directory", []string{filepath.Join(env.Dir, "svc")}, "svc/a.py:2:9: " + py012, "1 file"},
		{"a directory with no change", []string{".june"}, "", "0 files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runJune(t, env, check.Request{Base: "main", Paths: tt.paths})
			wantExit := 1
			if tt.want == "" {
				wantExit = 0
			}
			expect(t, res, wantExit, tt.want)
			if !strings.Contains(string(res.Out), "checked "+tt.files) {
				t.Errorf("summary does not say checked %s:\n%s", tt.files, res.Out)
			}
			if strings.Contains(string(res.Out), "excluded") {
				t.Errorf("narrowing is reported as exclusion:\n%s", res.Out)
			}
		})
	}
}

func TestPathsNarrowASuppliedList(t *testing.T) {
	env := tree(t, map[string]string{"svc/a.py": old, "lib/b.py": old})
	res := changeWith(t, env, nil, "svc/a.py:1\nlib/b.py:1\n")
	expect(t, res, 1, "lib/b.py:1:9: "+py012+"svc/a.py:1:9: "+py012)
	narrowed, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("svc/a.py:1\nlib/gone.py:1\n"), Paths: []string{"svc"}})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, narrowed, 1, "svc/a.py:1:9: "+py012)
}

func TestBadPathsAreRefused(t *testing.T) {
	env := twoPackages(t, "")
	sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
	refused(t, runJune(t, env, check.Request{Base: "main", Paths: []string{"nope"}}),
		"june: error: the path \"nope\" does not exist. Fix: pass an existing file or directory relative to the working directory\n")
	refused(t, runJune(t, sub, check.Request{Base: "main", Paths: []string{"../lib"}}),
		"june: error: the path \"../lib\" is outside the working directory. Fix: pass files or directories under the working directory, or run june from a directory that holds them\n")
}

func TestConfigBaseIsTheLowestPriorityBase(t *testing.T) {
	env := twoPackages(t, "base: main\n")
	if res := runJune(t, env, check.Request{}); !strings.Contains(string(res.Out), "base main (base in .june/config.yml)") {
		t.Errorf("config base unused:\n%s", res.Out)
	}
	if res := runJune(t, env, check.Request{Base: "HEAD"}); !strings.Contains(string(res.Out), "base HEAD (--base)") {
		t.Errorf("--base does not win over the config:\n%s", res.Out)
	}
	if res := runJune(t, env, check.Request{Changed: strings.NewReader("svc/a.py:2\n")}); strings.Contains(string(res.Out), "base main") {
		t.Errorf("config base used with a supplied list:\n%s", res.Out)
	}
	env.Vars = map[string]string{"PRE_COMMIT_FROM_REF": "HEAD", "PRE_COMMIT_TO_REF": "HEAD"}
	if res := runJune(t, env, check.Request{}); !strings.Contains(string(res.Out), "(pre-commit refs)") {
		t.Errorf("pre-commit refs do not win over the config base:\n%s", res.Out)
	}
}

func TestConfigRulesReplaceTheDefaultAndFlagsReplaceConfig(t *testing.T) {
	env := twoPackages(t, "rules:\n  - ci-rules\n")
	copyDir(t, filepath.Join("..", "..", "testdata", "rules", "python"), filepath.Join(env.Dir, "ci-rules"))
	sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
	expect(t, runJune(t, sub, check.Request{Base: "main"}), 1, "a.py:2:9: "+py012)
	if err := os.RemoveAll(filepath.Join(env.Dir, ".june", "rules")); err != nil {
		t.Fatal(err)
	}
	expect(t, runJune(t, env, check.Request{Base: "main", Paths: []string{"svc"}}), 1, "svc/a.py:2:9: "+py012)
	res := runJune(t, env, check.Request{Base: "main", Rules: []string{"missing"}})
	if res.Exit != 2 {
		t.Errorf("--rules did not replace the config rules: Exit = %d\n%s", res.Exit, res.Out)
	}
}

func TestConfigExcludesAreRelativeToTheConfigDirectory(t *testing.T) {
	env := twoPackages(t, "exclude:\n  - svc/a.py\n")
	sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
	res := runJune(t, sub, check.Request{Base: "main"})
	expect(t, res, 0, "")
	if !strings.Contains(string(res.Out), "1 file excluded") {
		t.Errorf("exclusion not reported:\n%s", res.Out)
	}
	expect(t, runJune(t, env, check.Request{Base: "main"}), 1, "lib/b.py:2:9: "+py012)
}

func TestUnknownConfigKeyIsRefused(t *testing.T) {
	env := twoPackages(t, "severity: error\n")
	res := runJune(t, env, check.Request{Base: "main"})
	if res.Exit != 2 || !strings.Contains(string(res.Out), "keys are rules (a list of directories), base (a git ref) and exclude (a list of globs)") {
		t.Errorf("Exit = %d, Out = %q", res.Exit, res.Out)
	}
}

func TestInitWritesAStarterThatVerifiesAndIsIdempotent(t *testing.T) {
	env := check.Env{Dir: t.TempDir()}
	git(t, env.Dir, "init", "-q", "-b", "main")
	res, err := check.Init(context.Background(), env, check.InitRequest{Version: "1.2.3"})
	if err != nil || res.Exit != 0 {
		t.Fatalf("Init = %d %q, %v", res.Exit, res.Out, err)
	}
	for _, rel := range []string{".june/rules/MY001.yml", ".june/rules/rule-tests/MY001-test.yml", ".june/config.yml", ".github/workflows/june.yml", ".june/gitlab-ci.yml", ".pre-commit-config.yaml"} {
		if _, err := os.Stat(filepath.Join(env.Dir, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(env.Dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read(".github/workflows/june.yml"); !strings.Contains(got, "uses: vicolby/june@v1.2.3") || !strings.Contains(got, "fetch-depth: 0") {
		t.Errorf("workflow:\n%s", got)
	}
	if got := read(".june/gitlab-ci.yml"); !strings.Contains(got, "releases/download/v1.2.3/install.sh | JUNE_VERSION=1.2.3 JUNE_INSTALL_AST_GREP=1 sh") || !strings.Contains(got, "GIT_DEPTH: 0") {
		t.Errorf("gitlab:\n%s", got)
	}
	if got := read(".pre-commit-config.yaml"); !strings.Contains(got, "rev: v1.2.3") || !strings.Contains(got, "id: june") {
		t.Errorf("pre-commit:\n%s", got)
	}
	if v, err := check.Verify(context.Background(), env, check.VerifyRequest{}); err != nil || v.Exit != 0 {
		t.Errorf("the starter rule does not verify: %d %q, %v", v.Exit, v.Out, err)
	}
	if c, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("")}); err != nil || c.Exit != 0 {
		t.Errorf("the starter config is not accepted: %d %q, %v", c.Exit, c.Out, err)
	}

	// A second run, with edits in place, keeps every file.
	write(t, filepath.Join(env.Dir, ".pre-commit-config.yaml"), "mine\n")
	write(t, filepath.Join(env.Dir, ".june/rules/MY001.yml"), "mine\n")
	again, err := check.Init(context.Background(), env, check.InitRequest{Version: "1.2.3"})
	if err != nil || again.Exit != 0 || strings.Contains(string(again.Out), "created") {
		t.Fatalf("second Init = %d %q, %v", again.Exit, again.Out, err)
	}
	if read(".pre-commit-config.yaml") != "mine\n" || read(".june/rules/MY001.yml") != "mine\n" {
		t.Error("a second Init overwrote a file")
	}

	// Force replaces the generated snippets, never the rules.
	forced, err := check.Init(context.Background(), env, check.InitRequest{Version: "1.2.3", Force: true})
	if err != nil || forced.Exit != 0 {
		t.Fatalf("forced Init = %d %q, %v", forced.Exit, forced.Out, err)
	}
	if !strings.Contains(read(".pre-commit-config.yaml"), "id: june") || read(".june/rules/MY001.yml") != "mine\n" {
		t.Error("--force did not replace the hook config, or touched the rules")
	}
}

func TestInitFromASubdirectoryWritesAtTheRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	write(t, filepath.Join(root, "svc/a.py"), "x = 1\n")
	res, err := check.Init(context.Background(), check.Env{Dir: filepath.Join(root, "svc")}, check.InitRequest{})
	if err != nil || res.Exit != 0 {
		t.Fatalf("Init = %d %q, %v", res.Exit, res.Out, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".june/rules/MY001.yml")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(root, "svc/.june")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Init wrote below the working directory: %v", err)
	}
	if !strings.Contains(string(res.Out), "vX.Y.Z") {
		t.Errorf("a build without a release version does not say the pin is a placeholder:\n%s", res.Out)
	}
}
