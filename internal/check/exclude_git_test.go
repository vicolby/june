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

// Exclusion happens after git discovery, so rename pairing is exactly what it is without it.
func TestExcludedPathsDoNotChangeRenamePairing(t *testing.T) {
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
