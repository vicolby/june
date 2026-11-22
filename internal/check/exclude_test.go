package check_test

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/check"
)

const badDefault = "def f(a=[]):\n    pass\n"

func changeReq(t *testing.T, env check.Env, req check.Request, list string) check.Result {
	t.Helper()
	req.Changed = strings.NewReader(list)
	res, err := check.Change(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestExcludeGlobs(t *testing.T) {
	files := []string{"app.py", "vendor/lib/a.py", "src/gen/x_pb2.py", "src/gen/y.py", "migrations/0001.py", "tests/fixtures/f.py", "a/b/c.py"}
	tests := []struct {
		glob string
		kept []string
	}{
		{"vendor", []string{"app.py", "src/gen/x_pb2.py", "src/gen/y.py", "migrations/0001.py", "tests/fixtures/f.py", "a/b/c.py"}},
		{"vendor/**", []string{"app.py", "src/gen/x_pb2.py", "src/gen/y.py", "migrations/0001.py", "tests/fixtures/f.py", "a/b/c.py"}},
		{"./vendor/", []string{"app.py", "src/gen/x_pb2.py", "src/gen/y.py", "migrations/0001.py", "tests/fixtures/f.py", "a/b/c.py"}},
		{"**/*_pb2.py", []string{"app.py", "vendor/lib/a.py", "src/gen/y.py", "migrations/0001.py", "tests/fixtures/f.py", "a/b/c.py"}},
		{"*_pb2.py", files},   // anchored at the working directory: * never crosses a slash
		{"app.py", files[1:]}, // an exact file
		{"src/*/y.py", append(append([]string{}, files[:3]...), files[4:]...)},
		{"**/fixtures/**", append(append([]string{}, files[:5]...), files[6:]...)},
		{"a/**/c.py", files[:6]},
		{"a/b/**/c.py", files[:6]},
		{"vend", files}, // a segment must match whole
	}
	for _, tt := range tests {
		t.Run(tt.glob, func(t *testing.T) {
			content := map[string]string{}
			var list strings.Builder
			for _, f := range files {
				content[f] = badDefault
				list.WriteString(f + ":1\n")
			}
			env := tree(t, content)
			res := changeReq(t, env, check.Request{Exclude: []string{tt.glob}}, list.String())
			var want strings.Builder
			for _, f := range sorted(tt.kept) {
				want.WriteString(f + ":1:9: " + py012)
			}
			if got := findingsOf(t, res); got != want.String() {
				t.Errorf("findings =\n%s\nwant\n%s", got, want.String())
			}
			if n := len(files) - len(tt.kept); n > 0 {
				if !strings.Contains(summaryOf(res), "; checked "+plural(len(tt.kept), "file")) || !strings.Contains(summaryOf(res), plural(n, "file")+" excluded") {
					t.Errorf("summary = %q, want %d checked and %d excluded", summaryOf(res), len(tt.kept), n)
				}
			}
		})
	}
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func TestConfigFileExcludes(t *testing.T) {
	env := tree(t, map[string]string{
		"app.py":           badDefault,
		"gen/a_pb2.py":     badDefault,
		".june/config.yml": "exclude:\n  - gen/**\n",
	})
	res := changeReq(t, env, check.Request{}, "app.py:1\ngen/a_pb2.py:1\n")
	expect(t, res, 1, "app.py:1:9: "+py012)
	if !strings.Contains(summaryOf(res), "1 file excluded") {
		t.Errorf("summary = %q, want one file excluded", summaryOf(res))
	}
}

func TestConfigAndFlagExcludesAdd(t *testing.T) {
	env := tree(t, map[string]string{
		"app.py": badDefault, "a/x.py": badDefault, "b/y.py": badDefault,
		".june/config.yml": "exclude: [a/**]\n",
	})
	res := changeReq(t, env, check.Request{Exclude: []string{"b/**"}}, "app.py:1\na/x.py:1\nb/y.py:1\n")
	expect(t, res, 1, "app.py:1:9: "+py012)
}

// An excluded file is never read, so one that is not UTF-8 cannot refuse the run and a huge one costs nothing.
func TestExcludedFileIsNeverRead(t *testing.T) {
	env := tree(t, map[string]string{"app.py": "x = 1\n", "gen/latin.py": "# caf\xe9\nx = []\n"})
	res := changeReq(t, env, check.Request{}, "app.py:1\ngen/latin.py:1\n")
	if res.Exit != 2 {
		t.Fatalf("without an exclude Exit = %d, want 2 (not UTF-8)", res.Exit)
	}
	res = changeReq(t, env, check.Request{Exclude: []string{"gen/**"}}, "app.py:1\ngen/latin.py:1\n")
	expect(t, res, 0, "")
}

func TestExcludeEverythingChecksNothing(t *testing.T) {
	env := tree(t, map[string]string{"app.py": badDefault})
	res := changeReq(t, env, check.Request{Exclude: []string{"**"}}, "app.py:1\n")
	expect(t, res, 0, "")
	if !strings.Contains(summaryOf(res), "checked 0 files") || !strings.Contains(summaryOf(res), "1 file excluded") {
		t.Errorf("summary = %q", summaryOf(res))
	}
}

func TestBadExcludeRefuses(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		exclude []string
		want    string
	}{
		{"empty glob", nil, []string{""}, `the exclude pattern "" is not a valid glob. Fix:`},
		{"absolute glob", nil, []string{"/etc/**"}, `the exclude pattern "/etc/**" is not a valid glob. Fix:`},
		{"unclosed class", nil, []string{"[a"}, `the exclude pattern "[a" is not a valid glob. Fix:`},
		{"bad config glob", map[string]string{".june/config.yml": "exclude: ['[a']\n"}, nil, `the exclude pattern "[a" is not a valid glob. Fix:`},
		{"unknown config key", map[string]string{".june/config.yml": "exclude: [a]\nseverity: off\n"}, nil, ".june/config.yml is not a valid june config ("},
		{"config not yaml", map[string]string{".june/config.yml": "exclude: [\n"}, nil, ".june/config.yml is not a valid june config ("},
		{"exclude not a list", map[string]string{".june/config.yml": "exclude: a\n"}, nil, ".june/config.yml is not a valid june config ("},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"app.py": badDefault}
			for k, v := range tt.files {
				files[k] = v
			}
			res := changeReq(t, tree(t, files), check.Request{Exclude: tt.exclude}, "app.py:1\n")
			if res.Exit != 2 || !strings.Contains(string(res.Out), "june: error: "+tt.want) {
				t.Errorf("Exit = %d, Out = %q, want exit 2 containing %q", res.Exit, res.Out, tt.want)
			}
		})
	}
}

func TestEmptyConfigFileIsFine(t *testing.T) {
	env := tree(t, map[string]string{"app.py": badDefault, ".june/config.yml": ""})
	expect(t, changeReq(t, env, check.Request{}, "app.py:1\n"), 1, "app.py:1:9: "+py012)
}

// A rule's files and ignores apply to the real paths of the files in the change.
func TestRuleFilesAndIgnoresApply(t *testing.T) {
	env := tree(t, map[string]string{
		"src/a.py": "x = [1]\n", "src/legacy/b.py": "x = [1]\n", "tests/c.py": "x = [1]\n", "vendor/src/d.py": "x = [1]\n",
	})
	rule := "id: SCOPE01\nlanguage: python\nseverity: error\nmessage: 'No lists. Fix: use a tuple.'\nfiles: ['src/**']\nignores: ['src/legacy/**']\nrule:\n  kind: list\n"
	write(t, filepath.Join(env.Dir, "scoped", "SCOPE01.yml"), rule)
	write(t, filepath.Join(env.Dir, "scoped", "rule-tests", "SCOPE01-test.yml"), "id: SCOPE01\nvalid: ['x = (1,)']\ninvalid: ['x = [1]']\n")
	res := changeWith(t, env, []string{"scoped"}, "src/a.py:1\nsrc/legacy/b.py:1\ntests/c.py:1\nvendor/src/d.py:1\n")
	expect(t, res, 1, "src/a.py:1:5: SCOPE01 No lists. Fix: use a tuple.\n")
}
