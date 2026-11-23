package check_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

func againstExcluding(t *testing.T, env check.Env, base string, globs ...string) check.Result {
	t.Helper()
	res, err := check.Change(context.Background(), env, check.Request{Base: base, Exclude: globs})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func mv(t *testing.T, env check.Env, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(env.Dir, to)), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, env.Dir, "mv", from, to)
}

// Exclusion preserves git's original rename pairing.
func TestExcludedPathsDoNotChangeRenamePairing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		from, to string
		edit     bool
		globs    []string
		want     string
	}{
		{"pure rename out of an excluded path", "vendor/old.py", "src/new.py", false, []string{"vendor/**"}, ""},
		{"pure rename into an excluded path", "src/old.py", "vendor/new.py", false, []string{"vendor/**"}, ""},
		{"edited rename out of an excluded path is judged", "vendor/old.py", "src/new.py", true, []string{"vendor/**"}, "src/new.py:4:9: " + py012},
		{"edited rename into an excluded path is not judged", "src/old.py", "vendor/new.py", true, []string{"vendor/**"}, ""},
		{"edited rename within an excluded tree", "vendor/old.py", "vendor/new.py", true, []string{"vendor/**"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := "import os\nimport sys\nx = 1\n" + old
			env := repo(t, map[string]string{tt.from: body})
			mv(t, env, tt.from, tt.to)
			if tt.edit {
				write(t, filepath.Join(env.Dir, filepath.FromSlash(tt.to)), strings.Replace(body, "x = 1", "x = 1\ndef g(b={}): pass", 1))
			}
			want := tt.want
			if tt.want == "" {
				expect(t, againstExcluding(t, env, "main", tt.globs...), 0, "")
				commit(t, env)
				expect(t, againstExcluding(t, env, "main", tt.globs...), 0, "")
				return
			}
			expect(t, againstExcluding(t, env, "main", tt.globs...), 1, want)
			commit(t, env)
			expect(t, againstExcluding(t, env, "main", tt.globs...), 1, want)
		})
	}
}

func TestExcludedEditProducesNoFindingsOnAGitRun(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"vendor/a.py": "x = 1\n", "app.py": "x = 1\n"})
	write(t, filepath.Join(env.Dir, "vendor", "a.py"), "x = 1\n"+badDefault)
	write(t, filepath.Join(env.Dir, "app.py"), "x = 1\n"+badDefault)
	expect(t, againstExcluding(t, env, "main", "vendor"), 1, "app.py:2:9: "+py012)
	commit(t, env)
	res := againstExcluding(t, env, "main", "vendor")
	expect(t, res, 1, "app.py:2:9: "+py012)
	if !strings.Contains(summaryOf(res), "checked 1 file,") || !strings.Contains(summaryOf(res), "1 file excluded") {
		t.Errorf("summary = %q", summaryOf(res))
	}
}

// A root exclude must cover a sibling of the working directory. Otherwise a
// repository-wide diff opens that excluded file and refuses.
func TestConfigExcludeMasksUnreadableSibling(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"svc", "svc/nested"} {
		t.Run(dir, func(t *testing.T) {
			env := repo(t, map[string]string{
				"svc/nested/app.py": "x = 1\n",
				"vendor/a.py":       "x = 1\n",
				".june/config.yml":  "exclude: [vendor/**]\n",
			})
			file := filepath.Join(env.Dir, "vendor", "a.py")
			write(t, file, "x = 22\n")
			write(t, filepath.Join(env.Dir, "svc", "nested", "app.py"), badDefault)
			if err := os.Chmod(file, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(file, 0o644) })
			if _, err := os.ReadFile(file); err == nil {
				t.Skip("permissions do not prevent reads on this platform")
			}
			sub := check.Env{Dir: filepath.Join(env.Dir, dir)}
			index := filepath.Join(env.Dir, ".git", "index")
			before, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			res := against(t, sub, "main")
			rel := "app.py"
			if dir == "svc" {
				rel = "nested/app.py"
			}
			expect(t, res, 1, rel+":1:9: "+py012)
			if !strings.Contains(summaryOf(res), "1 file excluded") {
				t.Errorf("summary = %q, want the sibling excluded", summaryOf(res))
			}
			after, err := os.ReadFile(index)
			if err != nil || string(after) != string(before) {
				t.Fatalf("check changed the caller's index: %v", err)
			}
		})
	}
}

// Excluded worktree files cannot prevent a verdict, even when git cannot read them.
func TestExcludedUnreadableGitPath(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"explicit", "inferred", "assume-unchanged", "skip-worktree", "untracked", "subdirectory", "staged", "refs"} {
		t.Run(mode, func(t *testing.T) {
			env := repo(t, map[string]string{"app.py": "x = 1\n", "vendor/a.py": "x = 1\n"})
			git(t, env.Dir, "branch", "--set-upstream-to=main")
			if mode == "assume-unchanged" {
				git(t, env.Dir, "update-index", "--assume-unchanged", "vendor/a.py")
			}
			if mode == "skip-worktree" {
				git(t, env.Dir, "update-index", "--skip-worktree", "vendor/a.py")
			}
			name := "a.py"
			if mode == "untracked" {
				name = "fresh.py"
			}
			file := filepath.Join(env.Dir, "vendor", name)
			write(t, file, "x = 2\n") // same-size edits must not trigger a content comparison
			write(t, filepath.Join(env.Dir, "app.py"), badDefault)
			if mode == "staged" || mode == "refs" {
				git(t, env.Dir, "add", "-A")
			}
			if mode == "refs" {
				git(t, env.Dir, "commit", "-qm", "change")
			}
			if err := os.Chmod(file, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(file, 0o644) })
			if _, err := os.ReadFile(file); err == nil {
				t.Skip("permissions do not prevent reads on this platform")
			}
			base := "main"
			if mode == "inferred" {
				base = ""
			}
			if mode == "staged" {
				base = ""
				env.Vars = map[string]string{"PRE_COMMIT": "1"}
			}
			if mode == "refs" {
				base = ""
				env.Vars = map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": "HEAD"}
			}
			if mode == "subdirectory" {
				copyDir(t, filepath.Join(env.Dir, ".june", "rules"), filepath.Join(env.Dir, "vendor", ".june", "rules"))
				write(t, filepath.Join(env.Dir, "vendor", "app.py"), badDefault)
				env.Dir = filepath.Join(env.Dir, "vendor")
			}
			index := filepath.Join(env.Dir, ".git", "index")
			if mode == "subdirectory" {
				index = filepath.Join(env.Dir, "..", ".git", "index")
			}
			before, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			globs := []string{"vendor/**"}
			if mode == "subdirectory" {
				globs = []string{"a.py"}
			}
			res := againstExcluding(t, env, base, globs...)
			expect(t, res, 1, "app.py:1:9: "+py012)
			after, err := os.ReadFile(index)
			if err != nil || string(after) != string(before) {
				t.Fatalf("check changed the caller's index: %v", err)
			}
		})
	}
}

// A deleted excluded sibling stays a rename source when the check runs elsewhere.
// A path that only shares a leading directory with the working directory is the same case.
func TestExcludedSiblingDeletionRemainsARenameSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, from, to, dir string
		files               map[string]string
	}{
		{
			name: "no shared directory",
			files: map[string]string{
				"vendor/old.py":    badDefault,
				"svc/keep.py":      "x = 1\n",
				".june/config.yml": "exclude: [vendor/**]\n",
			},
			from: "vendor/old.py", to: "svc/new.py", dir: "svc",
		},
		{
			name: "shared ancestor",
			files: map[string]string{
				"svc/legacy/old.py": badDefault,
				"svc/pkg/keep.py":   "x = 1\n",
				".june/config.yml":  "exclude: [svc/legacy/**]\n",
			},
			from: "svc/legacy/old.py", to: "svc/pkg/new.py", dir: "svc/pkg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := repo(t, tt.files)
			if err := os.Rename(filepath.Join(env.Dir, filepath.FromSlash(tt.from)), filepath.Join(env.Dir, filepath.FromSlash(tt.to))); err != nil {
				t.Fatal(err)
			}
			sub := check.Env{Dir: filepath.Join(env.Dir, tt.dir)}
			expect(t, against(t, sub, "main"), 0, "")
		})
	}
}

func TestExcludedDeletedPathRemainsARenameSource(t *testing.T) {
	t.Parallel()
	env := repo(t, map[string]string{"vendor/old.py": badDefault})
	if err := os.Rename(filepath.Join(env.Dir, "vendor", "old.py"), filepath.Join(env.Dir, "new.py")); err != nil {
		t.Fatal(err)
	}
	expect(t, againstExcluding(t, env, "main", "vendor/**"), 0, "")
}

// An excluded exact-match destination must retain its rename source, so fresh.py is all new.
func TestExcludedDestinationDoesNotHideNewFileViolations(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"local", "staged", "refs"} {
		t.Run(mode, func(t *testing.T) {
			env := repo(t, map[string]string{"legacy.py": badDefault})
			mv(t, env, "legacy.py", "vendor/legacy.txt")
			write(t, filepath.Join(env.Dir, "fresh.py"), badDefault+"x = 1\n")
			base := "main"
			if mode == "staged" || mode == "refs" {
				git(t, env.Dir, "add", "-A")
				base = ""
				env.Vars = map[string]string{"PRE_COMMIT": "1"}
			}
			if mode == "refs" {
				git(t, env.Dir, "commit", "-qm", "change")
				env.Vars = map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": "HEAD"}
			}
			expect(t, againstExcluding(t, env, base, "vendor/**"), 1, "fresh.py:1:9: "+py012)
		})
	}
}

// Leaf-pattern excludes must not expand into an unbounded list of git arguments.
func TestExcludedSevenThousandGitPaths(t *testing.T) {
	t.Parallel()
	env, _ := manyChanges(t, 7000)
	write(t, filepath.Join(env.Dir, "app.py"), badDefault)
	for _, mode := range []string{"local", "staged", "refs"} {
		t.Run(mode, func(t *testing.T) {
			base := "main"
			if mode == "staged" {
				git(t, env.Dir, "add", "-A")
				base = ""
				env.Vars = map[string]string{"PRE_COMMIT": "1"}
			}
			if mode == "refs" {
				git(t, env.Dir, "commit", "-qm", "change")
				base = ""
				env.Vars = map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": "HEAD"}
			}
			res := againstExcluding(t, env, base, "**/mod*.py")
			expect(t, res, 1, "app.py:1:9: "+py012)
			if !strings.Contains(summaryOf(res), "7000 files excluded") {
				t.Errorf("summary = %q, want 7000 excluded files", summaryOf(res))
			}
		})
	}
}
