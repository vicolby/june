package check_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
	"go.yaml.in/yaml/v3"
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

func TestNestedJuneDoesNotOverrideTheRepositoryRoot(t *testing.T) {
	env := twoPackages(t, "exclude: [lib]\n")
	write(t, filepath.Join(env.Dir, "svc/.june/config.yml"), "unknown: true\n")
	write(t, filepath.Join(env.Dir, "svc/.june/rules/none.txt"), "")
	sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
	expect(t, runJune(t, sub, check.Request{Base: "main"}), 1, "a.py:2:9: "+py012)
	if v, err := check.Verify(context.Background(), sub, check.VerifyRequest{}); err != nil || v.Exit != 0 {
		t.Fatalf("Verify = %d %q, %v", v.Exit, v.Out, err)
	}
	if n, err := check.New(context.Background(), sub, nil, "MY002"); err != nil || n.Exit != 0 {
		t.Fatalf("New = %d %q, %v", n.Exit, n.Out, err)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, ".june/rules/MY002.yml")); err != nil {
		t.Fatal(err)
	}
}

func TestNewDefaultsToRootBeforeJuneExists(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	write(t, filepath.Join(root, "svc/a.py"), "x = 1\n")
	sub := check.Env{Dir: filepath.Join(root, "svc")}
	res, err := check.New(context.Background(), sub, nil, "MY002")
	if err != nil || res.Exit != 0 {
		t.Fatalf("New = %d %q, %v", res.Exit, res.Out, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".june/rules/MY002.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sub.Dir, ".june")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New created nested .june: %v", err)
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
		{"dot before narrower path", []string{".", "svc"}, both, "2 files"},
		{"dot after narrower path", []string{"svc", "."}, both, "2 files"},
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
	for _, bad := range []string{"nope", "../lib"} {
		for _, paths := range [][]string{{bad}, {".", bad}, {bad, "."}} {
			sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
			res := runJune(t, sub, check.Request{Base: "main", Paths: paths})
			cause := "does not exist"
			if bad == "../lib" {
				cause = "outside the working directory"
			}
			if res.Exit != 2 || !strings.Contains(string(res.Out), cause) {
				t.Errorf("paths %v: exit %d, output %s", paths, res.Exit, res.Out)
			}
		}
	}
}

func TestConfigBaseYieldsToTheStagedSnapshot(t *testing.T) {
	env := twoPackages(t, "base: main\n")
	commit(t, env)
	write(t, filepath.Join(env.Dir, "safe.py"), "x = 1\n")
	git(t, env.Dir, "add", "safe.py")
	write(t, filepath.Join(env.Dir, "safe.py"), old)
	write(t, filepath.Join(env.Dir, "untracked.py"), old)
	env.Vars = map[string]string{"PRE_COMMIT": "1"}
	res := runJune(t, env, check.Request{})
	expect(t, res, 0, "")
	if !strings.Contains(string(res.Out), "checked 1 file, 1 changed line; base HEAD (staged diff)") {
		t.Fatalf("staged scope: %s", res.Out)
	}
	explicit := runJune(t, env, check.Request{Base: "main"})
	if explicit.Exit != 1 || !strings.Contains(string(explicit.Out), "base main (--base)") {
		t.Fatalf("explicit base: %d %s", explicit.Exit, explicit.Out)
	}
	write(t, filepath.Join(env.Dir, "safe.py"), old)
	git(t, env.Dir, "add", "safe.py")
	expect(t, runJune(t, env, check.Request{}), 1, "safe.py:1:9: "+py012)
	write(t, filepath.Join(env.Dir, ".june/config.yml"), "base: -invalid\n")
	expect(t, runJune(t, env, check.Request{}), 1, "safe.py:1:9: "+py012)
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
	for _, rel := range []string{".june/rules/MY001.yml", ".june/rules/rule-tests/MY001-test.yml", ".june/config.yml", ".github/workflows/june.yml", ".pre-commit-config.yaml"} {
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
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string
				With map[string]int
			}
		}
	}
	if err := yaml.Unmarshal([]byte(read(".github/workflows/june.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["june"].Steps
	if len(steps) != 2 || steps[0].Uses != "actions/checkout@v4" || steps[0].With["fetch-depth"] != 0 {
		t.Fatalf("checkout contract: %+v", steps)
	}
	if _, ok := steps[0].With["fetch-depth"]; !ok || steps[1].Uses != "vicolby/june@v1.2.3" {
		t.Fatalf("workflow contract: %+v", steps)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, ".june/gitlab-ci.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default init wrote GitLab config: %v", err)
	}
	var hooks struct {
		Repos []struct {
			Repo, Rev string
			Hooks     []struct{ ID string }
		}
	}
	if err := yaml.Unmarshal([]byte(read(".pre-commit-config.yaml")), &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks.Repos) != 1 || hooks.Repos[0].Repo != "https://github.com/vicolby/june" || hooks.Repos[0].Rev != "v1.2.3" || len(hooks.Repos[0].Hooks) != 1 || hooks.Repos[0].Hooks[0].ID != "june" {
		t.Fatalf("pre-commit contract: %+v", hooks)
	}
	if v, err := check.Verify(context.Background(), env, check.VerifyRequest{}); err != nil || v.Exit != 0 {
		t.Errorf("the starter rule does not verify: %d %q, %v", v.Exit, v.Out, err)
	}
	if c, err := check.Change(context.Background(), env, check.Request{Changed: strings.NewReader("")}); err != nil || c.Exit != 0 {
		t.Errorf("the starter config is not accepted: %d %q, %v", c.Exit, c.Out, err)
	}

	// A second run, with edits in place, keeps every file.
	write(t, filepath.Join(env.Dir, ".pre-commit-config.yaml"), "mine\n")
	write(t, filepath.Join(env.Dir, ".github/workflows/june.yml"), "mine\n")
	write(t, filepath.Join(env.Dir, ".june/config.yml"), "base: main\n")
	write(t, filepath.Join(env.Dir, ".june/rules/MY001.yml"), "mine\n")
	again, err := check.Init(context.Background(), env, check.InitRequest{Version: "1.2.3"})
	if err != nil || again.Exit != 0 || strings.Contains(string(again.Out), "created") {
		t.Fatalf("second Init = %d %q, %v", again.Exit, again.Out, err)
	}
	for _, rel := range []string{".pre-commit-config.yaml", ".github/workflows/june.yml", ".june/rules/MY001.yml"} {
		if read(rel) != "mine\n" {
			t.Errorf("a second Init overwrote %s", rel)
		}
	}
	if read(".june/config.yml") != "base: main\n" {
		t.Error("a second Init overwrote the config")
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

func TestNewUsesConfiguredRulesDirectories(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		dirs         []string
		want         string
		exit         int
	}{
		{"configured", "rules: [ci-rules]\n", nil, "ci-rules", 0},
		{"ambiguous", "rules: [ci-rules, other-rules]\n", nil, "", 2},
		{"duplicates", "rules: [ci-rules, ./ci-rules]\n", nil, "ci-rules", 0},
		{"explicit", "rules: [ci-rules, other-rules]\n", []string{"explicit"}, "svc/explicit", 0},
		{"empty explicit", "rules: [ci-rules]\n", []string{""}, "svc/.june/rules", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := twoPackages(t, tc.config)
			sub := check.Env{Dir: filepath.Join(env.Dir, "svc")}
			res, err := check.New(context.Background(), sub, tc.dirs, "MY002")
			if err != nil || res.Exit != tc.exit {
				t.Fatalf("New = %d %q, %v", res.Exit, res.Out, err)
			}
			if tc.exit == 2 {
				if _, err := os.Stat(filepath.Join(env.Dir, "ci-rules")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("ambiguous destination was written: %v", err)
				}
				return
			}
			if _, err := os.Stat(filepath.Join(env.Dir, tc.want, "MY002.yml")); err != nil {
				t.Fatal(err)
			}
			v, err := check.Verify(context.Background(), sub, check.VerifyRequest{Rules: tc.dirs, Rule: "MY002"})
			if err != nil || v.Exit != 0 {
				t.Fatalf("Verify = %d %q, %v", v.Exit, v.Out, err)
			}
		})
	}
}

func TestInitGitLabConsumerContract(t *testing.T) {
	env := check.Env{Dir: t.TempDir()}
	git(t, env.Dir, "init", "-q", "-b", "main")
	res, err := check.Init(context.Background(), env, check.InitRequest{CI: "gitlab", Version: "v1.2.3"})
	if err != nil || res.Exit != 0 {
		t.Fatalf("Init = %d %q, %v", res.Exit, res.Out, err)
	}
	data, err := os.ReadFile(filepath.Join(env.Dir, ".june/gitlab-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		June struct {
			Image        string
			Variables    map[string]string
			Rules        []struct{ If string }
			BeforeScript []string `yaml:"before_script"`
			Script       []string
			Artifacts    struct {
				When    string
				Reports map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	job := config.June
	if job.Image != "node:22" || job.Variables["GIT_DEPTH"] != "0" || len(job.Rules) != 1 || job.Rules[0].If != `$CI_PIPELINE_SOURCE == "merge_request_event"` {
		t.Fatalf("GitLab environment: %+v", job)
	}
	if len(job.BeforeScript) != 1 || job.BeforeScript[0] != "curl -fsSL https://github.com/vicolby/june/releases/download/v1.2.3/install.sh | JUNE_VERSION=1.2.3 JUNE_INSTALL_AST_GREP=1 sh" || len(job.Script) != 1 || job.Script[0] != "june" {
		t.Fatalf("GitLab commands: %+v", job)
	}
	if job.Artifacts.When != "always" || job.Artifacts.Reports["codequality"] != "gl-code-quality-report.json" {
		t.Fatalf("GitLab report: %+v", job.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, ".github/workflows/june.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("GitLab init wrote GitHub config: %v", err)
	}
}

func TestInitGuidanceForPreservedRules(t *testing.T) {
	env := check.Env{Dir: t.TempDir()}
	git(t, env.Dir, "init", "-q", "-b", "main")
	if res, err := check.New(context.Background(), env, nil, "CUSTOM"); err != nil || res.Exit != 0 {
		t.Fatalf("New = %d %q, %v", res.Exit, res.Out, err)
	}
	res, err := check.Init(context.Background(), env, check.InitRequest{Version: "1.2.3"})
	if err != nil || res.Exit != 0 || !strings.HasSuffix(string(res.Out), "next: run june verify to verify the preserved rules\n") {
		t.Fatalf("Init = %d %q, %v", res.Exit, res.Out, err)
	}
	v, err := check.Verify(context.Background(), env, check.VerifyRequest{})
	if err != nil || v.Exit != 0 {
		t.Fatalf("suggested verification = %d %q, %v", v.Exit, v.Out, err)
	}
}

func TestInitRefusesUnavailableRepositoryOrProvider(t *testing.T) {
	for _, ci := range []string{"", "unknown"} {
		env := check.Env{Dir: t.TempDir()}
		if ci != "" {
			git(t, env.Dir, "init", "-q", "-b", "main")
		}
		res, err := check.Init(context.Background(), env, check.InitRequest{CI: ci})
		if err != nil || res.Exit != 2 {
			t.Fatalf("Init = %d %q, %v", res.Exit, res.Out, err)
		}
		if _, err := os.Stat(filepath.Join(env.Dir, ".june")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused init wrote files: %v", err)
		}
	}
}

func TestEmptyConfiguredRulesResolveToRootDefaults(t *testing.T) {
	for _, config := range []string{"rules: ['']\n", "rules: ['', .june/rules]\n"} {
		t.Run(config, func(t *testing.T) {
			env := twoPackages(t, config)
			for _, tc := range []struct {
				name, dir, path, file, id string
			}{
				{"root", env.Dir, "svc", "svc/a.py", "MY002"},
				{"subdirectory", filepath.Join(env.Dir, "svc"), ".", "a.py", "MY003"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					caller := check.Env{Dir: tc.dir}
					expect(t, runJune(t, caller, check.Request{Base: "main", Paths: []string{tc.path}}), 1, tc.file+":2:9: "+py012)
					res, err := check.New(context.Background(), caller, nil, tc.id)
					if err != nil || res.Exit != 0 {
						t.Fatalf("New = %d %q, %v", res.Exit, res.Out, err)
					}
					for _, rel := range []string{tc.id + ".yml", "rule-tests/" + tc.id + "-test.yml"} {
						if _, err := os.Stat(filepath.Join(env.Dir, ".june/rules", rel)); err != nil {
							t.Fatal(err)
						}
						if _, err := os.Stat(filepath.Join(env.Dir, rel)); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("New wrote outside the rules directory: %s, %v", rel, err)
						}
					}
					v, err := check.Verify(context.Background(), caller, check.VerifyRequest{Rule: tc.id})
					if err != nil || v.Exit != 0 {
						t.Fatalf("Verify = %d %q, %v", v.Exit, v.Out, err)
					}
				})
			}
		})
	}
}

func TestPathsPreserveRenameCandidates(t *testing.T) {
	for _, mode := range []string{"untracked", "tracked", "assume-unchanged", "skip-worktree", "staged", "refs"} {
		t.Run(mode, func(t *testing.T) {
			body := strings.Repeat("x = 1\n", 50) + old
			env := repo(t, map[string]string{"old.py": body})
			if mode == "tracked" || mode == "assume-unchanged" || mode == "skip-worktree" {
				write(t, filepath.Join(env.Dir, "archive.py"), "y = 0\n")
				commit(t, env)
				if mode != "tracked" {
					git(t, env.Dir, "update-index", "--"+mode, "archive.py")
				}
			}
			if err := os.Rename(filepath.Join(env.Dir, "old.py"), filepath.Join(env.Dir, "archive.py")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(env.Dir, "selected/fresh.py"), body+"z = 2\n")
			base := "main"
			if mode == "staged" || mode == "refs" {
				git(t, env.Dir, "add", "-A")
				base = ""
				env.Vars = map[string]string{"PRE_COMMIT": "1"}
				if mode == "refs" {
					git(t, env.Dir, "commit", "-qm", "change")
					env.Vars = map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": "HEAD"}
				}
			}
			index := filepath.Join(env.Dir, ".git/index")
			before, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name           string
				paths, exclude []string
				clean          bool
			}{
				{"unrestricted", nil, nil, false},
				{"file", []string{"selected/fresh.py"}, nil, false},
				{"directory", []string{"selected"}, nil, false},
				{"absolute file", []string{filepath.Join(env.Dir, "selected/fresh.py")}, nil, false},
				{"unrelated exclusion", []string{"selected/fresh.py"}, []string{"ignored"}, false},
				{"renamed file", []string{"archive.py"}, nil, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					res := runJune(t, env, check.Request{Base: base, Paths: tc.paths, Exclude: tc.exclude})
					if tc.clean {
						expect(t, res, 0, "")
					} else {
						expect(t, res, 1, "selected/fresh.py:51:9: "+py012)
						if !strings.Contains(summaryOf(res), "checked 1 file, 52 changed lines") {
							t.Fatalf("fresh file was not wholly new: %s", res.Out)
						}
					}
					if strings.Contains(summaryOf(res), "excluded") {
						t.Fatalf("narrowing counted as exclusion: %s", res.Out)
					}
				})
			}
			after, err := os.ReadFile(index)
			if err != nil || string(after) != string(before) {
				t.Fatalf("check changed the caller's index: %v", err)
			}
		})
	}
}

func TestPathsDoNotCountUnselectedSymlinks(t *testing.T) {
	env := repo(t, map[string]string{"app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "app.py"), old)
	if err := os.Symlink("app.py", filepath.Join(env.Dir, "other.py")); err != nil {
		t.Fatal(err)
	}
	res := runJune(t, env, check.Request{Base: "main", Paths: []string{"app.py"}})
	expect(t, res, 1, "app.py:1:9: "+py012)
	if strings.Contains(summaryOf(res), "skipped") {
		t.Fatalf("unselected symlink counted: %s", res.Out)
	}
}
