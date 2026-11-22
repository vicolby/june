package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestActionPrefersPinnedASTGrep(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct{ Run, Shell string }
		}
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	if len(action.Runs.Steps) != 2 {
		t.Fatalf("steps = %d, want install then run", len(action.Runs.Steps))
	}
	pin, err := os.ReadFile(filepath.Join(root, ".ast-grep-version"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(pin))
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))
	for _, shadowed := range []bool{false, true} {
		name := "clean"
		if shadowed {
			name = "shadowed"
		}
		t.Run(name, func(t *testing.T) {
			tools, ambient, global := t.TempDir(), t.TempDir(), t.TempDir()
			runner := filepath.Join(t.TempDir(), "runner temp")
			if err := os.Mkdir(runner, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			pinned := filepath.Join(tools, "pinned-ast-grep")
			writeExecutable(pinned, "#!/bin/sh\necho 'ast-grep "+version+"'\n")
			writeExecutable(filepath.Join(tools, "npm"), `#!/bin/sh
set -eu
test "$*" = "install --global @ast-grep/cli@$EXPECTED_AST_GREP_VERSION"
prefix=${npm_config_prefix:-$FALLBACK_NPM_PREFIX}
mkdir -p "$prefix/bin"
cp "$PINNED_AST_GREP" "$prefix/bin/ast-grep"
`)
			if shadowed {
				writeExecutable(filepath.Join(ambient, "ast-grep"), "#!/bin/sh\necho 'ast-grep 0.51.1'\n")
				writeExecutable(filepath.Join(ambient, "june"), "#!/bin/sh\nexit 99\n")
			}
			path := strings.Join([]string{ambient, tools, filepath.Join(global, "bin"), "/usr/bin", "/bin"}, string(os.PathListSeparator))
			pathFile := filepath.Join(runner, "github-path")
			env := []string{
				"PATH=" + path, "RUNNER_TEMP=" + runner, "TMPDIR=" + runner,
				"GITHUB_PATH=" + pathFile, "GITHUB_ACTION_PATH=" + root,
				"INPUT_VERSION=0.2.0", "ACTION_REF=v0.2.0", "INPUT_RULES=.june/rules", "INPUT_BASE=HEAD",
				"JUNE_RELEASES_URL=" + srv.URL, "JUNE_OS=linux", "JUNE_ARCH=amd64",
				"EXPECTED_AST_GREP_VERSION=" + version, "PINNED_AST_GREP=" + pinned, "FALLBACK_NPM_PREFIX=" + global,
			}
			for i, step := range action.Runs.Steps {
				if step.Shell != "bash" {
					t.Fatalf("shell = %q, want bash", step.Shell)
				}
				cmd := exec.CommandContext(context.Background(), "bash", "-e", "-c", step.Run)
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("action step %d: %v\n%s", i, err, out)
				}
				if i == 0 {
					paths, err := os.ReadFile(pathFile)
					if err != nil {
						t.Fatal(err)
					}
					for _, dir := range strings.Split(strings.TrimSuffix(string(paths), "\n"), "\n") {
						path = dir + string(os.PathListSeparator) + path
					}
					env[0] = "PATH=" + path
				}
			}
			cmd := exec.CommandContext(context.Background(), "bash", "-c", "ast-grep --version")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if want := "ast-grep " + version; err != nil || strings.TrimSpace(string(out)) != want {
				t.Fatalf("selected ast-grep = %q, %v, want %q", out, err, want)
			}
		})
	}
}
