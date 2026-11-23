package check_test

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
	"github.com/vicolby/june/internal/diag"
)

// vendor is one place june runs: how it sets up the checkout's environment, and which native files it may add.
type vendor struct {
	name string
	// env turns a feature branch that tracks origin/main into the environment this vendor gives june.
	env func(t *testing.T, clone check.Env) check.Env
	// native reports whether a line of Out belongs to the vendor's own report rather than the shared text.
	native func(line string) bool
	// reports is the set of report files the vendor may write.
	reports []string
}

var vendors = []vendor{
	{
		name: "dev shell",
		env:  func(_ *testing.T, clone check.Env) check.Env { return clone },
	},
	{
		name:   "github",
		env:    func(_ *testing.T, clone check.Env) check.Env { return onGitHub(clone, "pull_request", "main") },
		native: func(line string) bool { return strings.HasPrefix(line, "::") },
	},
	{
		name: "gitlab",
		env: func(t *testing.T, clone check.Env) check.Env {
			return mergeRequest(clone, revParse(t, clone.Dir, "origin/main"))
		},
		reports: []string{diag.GitLabReport},
	},
}

// baseSource matches the "; base <ref> (<source>)" clause only on a summary line.
// The base label is the one intentional non-report difference between environments,
// because each vendor resolves its base differently. Exit code and findings stay compared exactly.
var baseSource = regexp.MustCompile(`(?m)^(june: \d+ errors?, \d+ warnings?, \d+ opt-outs?; checked \d+ files?, \d+ changed lines?); base \S+ \([a-z -]+\)`)

func scenarioChange(t *testing.T, files map[string]string, edit map[string]string) check.Env {
	t.Helper()
	origin := repo(t, files)
	env := clone(t, origin)
	git(t, env.Dir, "checkout", "-q", "-b", "feature", "--track", "origin/main")
	for name, body := range edit {
		write(t, filepath.Join(env.Dir, name), body)
	}
	commit(t, env)
	return env
}

// verdictOf is what every vendor must agree on: the exit code, the diagnostics and the text around them.
type verdictOf struct {
	exit  int
	diags []diag.Diagnostic
	text  string
}

func judge(t *testing.T, v vendor, clone check.Env) (verdictOf, check.Result) {
	t.Helper()
	res := infer(t, v.env(t, clone))
	for _, f := range res.Files {
		if !contains(v.reports, f.Path) {
			t.Errorf("%s wrote %s, want only %v", v.name, f.Path, v.reports)
		}
	}
	var text []string
	for _, l := range strings.SplitAfter(string(res.Out), "\n") {
		if l != "" && (v.native == nil || !v.native(l)) {
			text = append(text, l)
		}
	}
	return verdictOf{exit: res.Exit, diags: res.Diags, text: baseSource.ReplaceAllString(strings.Join(text, ""), "$1")}, res
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestEveryVendorReachesTheSameVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		edit  map[string]string
		exit  int
		want  string // the shared text, without the summary's base
	}{
		{
			name:  "a violation on a touched line fails",
			files: map[string]string{"app.py": "x = 1\n"},
			edit:  map[string]string{"app.py": "x = 1\n" + old, "pkg/more.py": "def g(b={}): pass\n"},
			exit:  1,
			want: "app.py:2:9: " + py012 + "pkg/more.py:1:9: " + py012 +
				"june: 2 errors, 0 warnings, 0 opt-outs; checked 2 files, 2 changed lines\n",
		},
		{
			name:  "an opt-out is reported and suppresses its finding",
			files: map[string]string{"app.py": "x = 1\n"},
			edit:  map[string]string{"app.py": "x = 1\ndef f(a=[]): pass  # june: ignore[PY012] shared on purpose\n"},
			exit:  0,
			want: "app.py:2:20: PY012 opt-out: shared on purpose\n" +
				"june: 0 errors, 0 warnings, 1 opt-out; checked 1 file, 1 changed line\n",
		},
		{
			name:  "a violation on an untouched line passes",
			files: map[string]string{"app.py": old},
			edit:  map[string]string{"other.py": "y = 1\n"},
			exit:  0,
			want:  "june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line\n",
		},
		{
			name:  "a clean change passes",
			files: map[string]string{"app.py": "x = 1\n"},
			edit:  map[string]string{"app.py": "x = 2\n"},
			exit:  0,
			want:  "june: 0 errors, 0 warnings, 0 opt-outs; checked 1 file, 1 changed line\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clone := scenarioChange(t, tc.files, tc.edit)
			var first verdictOf
			for i, v := range vendors {
				got, _ := judge(t, v, clone)
				if got.exit != tc.exit {
					t.Errorf("%s: Exit = %d, want %d", v.name, got.exit, tc.exit)
				}
				if got.text != tc.want {
					t.Errorf("%s: text =\n%s\nwant\n%s", v.name, got.text, tc.want)
				}
				if i == 0 {
					first = got
					continue
				}
				if !reflect.DeepEqual(got.diags, first.diags) {
					t.Errorf("%s: diagnostics differ from %s:\n%v\nvs\n%v", v.name, vendors[0].name, got.diags, first.diags)
				}
			}
		})
	}
}
