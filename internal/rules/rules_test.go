package rules_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/rules"
)

func fixture(name string) string {
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return abs
}

func TestLoadGood(t *testing.T) {
	rs, faults := rules.Load(context.Background(), fixture("good"))
	if len(faults) != 0 || rs == nil {
		t.Fatalf("Load = %v, %v, want a rule set", rs, faults)
	}
}

func TestLoadRefusesUnverifiedRules(t *testing.T) {
	tests := []struct {
		dir       string
		wantPath  string
		wantCause string
	}{
		{"bad_example", "rule-tests/LST01-test.yml", "example of rule LST01 misbehaves"},
		{"empty_message", "LST01.yml", "empty message"},
		{"no_examples", "LST01.yml", "no examples"},
		{"one_sided", "rule-tests/LST01-test.yml", "both valid and invalid"},
		{"unparseable", "LST01.yml", "cannot parse the rule"},
		{"empty", "", "holds no rules"},
		{"missing", "", "does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			rs, faults := rules.Load(context.Background(), fixture(tt.dir))
			if rs != nil {
				t.Fatal("Load returned a rule set alongside faults")
			}
			if len(faults) == 0 {
				t.Fatal("Load returned no faults")
			}
			f := faults[0]
			if f.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", f.Path, tt.wantPath)
			}
			if !strings.Contains(f.Cause, tt.wantCause) {
				t.Errorf("Cause = %q, want it to contain %q", f.Cause, tt.wantCause)
			}
			if f.Fix == "" {
				t.Error("Fix is empty")
			}
			for _, s := range []string{f.Path, f.Cause, f.Fix} {
				if strings.Contains(s, os.TempDir()) || strings.Contains(s, fixture(".")) {
					t.Errorf("fault leaks an absolute path: %q", s)
				}
			}
		})
	}
}

func TestLoadMissingAstGrep(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rs, faults := rules.Load(context.Background(), fixture("good"))
	if rs != nil || len(faults) != 1 || !strings.Contains(faults[0].Cause, "ast-grep was not found") {
		t.Fatalf("Load = %v, %v, want one missing ast-grep fault", rs, faults)
	}
}

func TestScanAnchorsAtFirstLine(t *testing.T) {
	rs := loadGood(t)
	dir := t.TempDir()
	py := filepath.Join(dir, "a.py")
	write(t, py, "x = (1,\n     [2,\n      3],\n     4)\ny = []\n")
	txt := filepath.Join(dir, "notes.txt")
	write(t, txt, "x = []\n")

	got, err := rs.Scan(context.Background(), []string{py, txt})
	if err != nil {
		t.Fatal(err)
	}
	type at struct {
		file      string
		line, col int
	}
	var have []at
	for _, m := range got.Matches {
		if m.Rule != "LST01" || m.Severity != "error" || !strings.Contains(m.Msg, "Fix: use a tuple") {
			t.Errorf("match = %+v, want rule LST01, severity error and the message", m)
		}
		have = append(have, at{m.File, m.Line, m.Col})
	}
	want := []at{{py, 2, 6}, {py, 5, 5}}
	if len(have) != len(want) || have[0] != want[0] || have[1] != want[1] {
		t.Errorf("matches at %v, want %v", have, want)
	}
}

func TestScanReadsOnlyGivenPythonFiles(t *testing.T) {
	rs := loadGood(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "other.py"), "x = [1]\n")
	got, err := rs.Scan(context.Background(), nil)
	if err != nil || len(got.Matches) != 0 {
		t.Errorf("Scan(nil) = %+v, %v, want nothing", got, err)
	}
	got, err = rs.Scan(context.Background(), []string{filepath.Join(dir, "notes.md")})
	if err != nil || len(got.Matches) != 0 {
		t.Errorf("Scan(non-python) = %+v, %v, want nothing", got, err)
	}
}

func TestScanCancelled(t *testing.T) {
	rs := loadGood(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	py := filepath.Join(t.TempDir(), "a.py")
	write(t, py, "x = [1]\n")
	if _, err := rs.Scan(ctx, []string{py}); err == nil {
		t.Error("Scan with a cancelled context returned no error")
	}
}

func loadGood(t *testing.T) *rules.RuleSet {
	t.Helper()
	rs, faults := rules.Load(context.Background(), fixture("good"))
	if rs == nil {
		t.Fatalf("Load: %v", faults)
	}
	return rs
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsMismatchedIdentityAndExtraDocuments(t *testing.T) {
	rule, err := os.ReadFile(filepath.Join(fixture("good"), "LST01.yml"))
	if err != nil {
		t.Fatal(err)
	}
	examples, err := os.ReadFile(filepath.Join(fixture("good"), "rule-tests", "LST01-test.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, rule, examples, other, cause string
	}{
		{"mismatched id", string(rule), strings.ReplaceAll(string(examples), "LST01", "OTHER"), "", "id does not match"},
		{"missing id", string(rule), "valid: ['x = (1,)']\ninvalid: ['x = [1]']\n", "", "id does not match"},
		{"other loaded rule id", string(rule), string(examples), strings.ReplaceAll(strings.ReplaceAll(string(rule), "LST01", "OTHER"), "kind: list", "kind: tuple"), "id does not match"},
		{"extra rule", string(rule) + "\n---\n" + strings.ReplaceAll(string(rule), "LST01", "EXTRA"), string(examples), "", "one valid YAML document"},
		{"empty extra rule", string(rule) + "\n---\n", string(examples), "", "one valid YAML document"},
		{"extra examples", string(rule), string(examples) + "\n---\nid: LST01\nvalid: ['x = []']\n", "", "one valid YAML document"},
		{"empty extra examples", string(rule), string(examples) + "\n---\n", "", "one valid YAML document"},
		{"duplicate rule id", string(rule), string(examples), string(rule), "duplicated"},
		{"path in id", strings.ReplaceAll(string(rule), "LST01", "../OTHER"), string(examples), "", "not a filename"},
		{"other language", strings.ReplaceAll(string(rule), "python", "javascript"), string(examples), "", "not Python"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "LST01.yml"), tc.rule)
			write(t, filepath.Join(dir, "rule-tests", "LST01-test.yml"), tc.examples)
			if tc.other != "" {
				write(t, filepath.Join(dir, "OTHER.yml"), tc.other)
				write(t, filepath.Join(dir, "rule-tests", "OTHER-test.yml"), string(examples))
			}
			rs, faults := rules.Load(context.Background(), dir)
			if rs != nil || len(faults) == 0 || !strings.Contains(faults[0].Cause, tc.cause) {
				t.Fatalf("Load = %v, %v, want refusal containing %q", rs, faults, tc.cause)
			}
		})
	}
}

func TestScanRefusesIncompleteInput(t *testing.T) {
	rs := loadGood(t)
	for _, name := range []string{"missing", "unreadable", "invalid UTF-8", "NUL", "directory"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			good := filepath.Join(dir, "good.py")
			bad := filepath.Join(dir, "bad.py")
			write(t, good, "x = []\n")
			switch name {
			case "unreadable":
				write(t, bad, "x = []\n")
				if err := os.Chmod(bad, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(bad, 0o644); err != nil {
						t.Error(err)
					}
				})
			case "invalid UTF-8":
				write(t, bad, "# coding: latin-1\nx = '\xe9'\ny = []\n")
			case "NUL":
				write(t, bad, "\x00\nx = []\n")
			case "directory":
				if err := os.Mkdir(bad, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			got, err := rs.Scan(context.Background(), []string{good, bad})
			if err == nil || len(got.Matches) != 0 {
				t.Fatalf("Scan = %+v, %v, want an error and no partial matches", got, err)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("error exposes an absolute path: %v", err)
			}
		})
	}
}

func TestScanLargeFile(t *testing.T) {
	rs := loadGood(t)
	py := filepath.Join(t.TempDir(), "large.py")
	write(t, py, "#"+strings.Repeat(" ", 4*1024*1024)+"\nx = []\n")
	got, err := rs.Scan(context.Background(), []string{py})
	if err != nil || len(got.Matches) != 1 || got.Matches[0].Line != 2 {
		t.Fatalf("Scan = %+v, %v, want the finding after the large comment", got, err)
	}
}

func TestScanPreservesDirectiveTextInStrings(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "TEXT01.yml"), "id: TEXT01\nlanguage: python\nseverity: error\nmessage: 'Fix: use other text'\nrule:\n  kind: string\n  regex: ast-grep-ignore\n")
	write(t, filepath.Join(dir, "rule-tests", "TEXT01-test.yml"), "id: TEXT01\nvalid: ['x = 1']\ninvalid: [\"x = 'ast-grep-ignore'\"]\n")
	rs, faults := rules.Load(context.Background(), dir)
	if rs == nil {
		t.Fatalf("Load: %v", faults)
	}
	py := filepath.Join(dir, "app.py")
	source := "x = '# ast-grep-ignore'\ntext = '''\n# ast-grep-ignore\n'''\n# ast-grep-ignore\n"
	write(t, py, source)
	got, err := rs.Scan(context.Background(), []string{py})
	if err != nil || len(got.Matches) != 2 || got.Matches[0].Line != 1 || got.Matches[1].Line != 2 {
		t.Fatalf("Scan = %+v, %v, want both original string literals", got, err)
	}
	data, err := os.ReadFile(py)
	if err != nil || string(data) != source {
		t.Fatalf("source file changed: %q, %v", data, err)
	}
}

func TestScanUnavailableEngine(t *testing.T) {
	bin, err := exec.LookPath("ast-grep")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "ast-grep")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	rs := loadGood(t)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(dir, "app.py")
	write(t, py, "x = []\n")
	got, err := rs.Scan(context.Background(), []string{py})
	if err == nil || len(got.Matches) != 0 || strings.Contains(err.Error(), dir) {
		t.Fatalf("Scan = %+v, %v, want a refusal without absolute paths", got, err)
	}
}

func TestRunIgnoresProjectConfigInWorkingDirectory(t *testing.T) {
	rs := loadGood(t)
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "sgconfig.yml"), "ruleDirs: [rules]\nutilDirs: [missing]\n")
	t.Chdir(cwd)

	py := filepath.Join(t.TempDir(), "a.py")
	write(t, py, "x = [1]  # ast-grep-ignore\ny = [2]\n")
	got, err := rs.Scan(context.Background(), []string{py})
	if err != nil {
		t.Fatalf("Scan with a project sgconfig.yml in the working directory: %v", err)
	}
	if len(got.Matches) != 2 {
		t.Errorf("matches = %+v, want both lists", got.Matches)
	}
}

func TestScanFindsOptOuts(t *testing.T) {
	rs := loadGood(t)
	dir := t.TempDir()
	py := filepath.Join(dir, "a.py")
	write(t, py, `a = []  # june: ignore[LST01] legacy shape
b = []  # june:ignore[LST01] no space
c = []  #june: ignore[LST01] why
d = []  # june: ignore[LST01]
e = []  # june: ignore[]  why
f = []  # june: ignore[LST01, LST02] why
g = []  # june: ignore[NOPE] why
h = []  # june: ignore LST01 why
i = []  # june: ignore[LST01 why
j = []  # june: suppress[LST01] why
k = "# june: ignore[LST01] in a string"
l = []  # note june: ignore[LST01] not at the start
m = []  # ast-grep-ignore
n = []  # june: ignore[ LST01] why
o = []  # june: ignore[LST01 ] why
p = []  # june: ignore[LST01]why
q = []  #june:ignore[ LST01 ]why
r = []  #	june: ignore[LST01] why
s = []  # june:	ignore[LST01] why
t = []  # june: ignore[LST01]	why
u = []  #  june: ignore[LST01] why
v = []  # june:  ignore[LST01] why
w = []  # june: ignore[	LST01	] why
`)
	got, err := rs.Scan(context.Background(), []string{py})
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		line         int
		rule, reason string
		bad          bool
	}
	var have []row
	for _, o := range got.OptOuts {
		if o.File != py {
			t.Errorf("opt-out file = %q, want %q", o.File, py)
		}
		if o.Col != 9 {
			t.Errorf("line %d: Col = %d, want 9", o.Line, o.Col)
		}
		if (o.Problem != "") && !strings.Contains(o.Problem, "Fix:") {
			t.Errorf("line %d: Problem %q has no Fix", o.Line, o.Problem)
		}
		have = append(have, row{o.Line, o.Rule, o.Reason, o.Problem != ""})
	}
	want := []row{
		{1, "LST01", "legacy shape", false},
		{2, "", "", true},
		{3, "", "", true},
		{4, "LST01", "", true},
		{5, "", "", true},
		{6, "", "", true},
		{7, "NOPE", "", true},
		{8, "", "", true},
		{9, "", "", true},
		{10, "", "", true},
		{14, "", "", true},
		{15, "", "", true},
		{16, "LST01", "", true},
		{17, "", "", true},
		{18, "", "", true},
		{19, "", "", true},
		{20, "LST01", "", true},
		{21, "", "", true},
		{22, "", "", true},
		{23, "", "", true},
	}
	if fmt.Sprint(have) != fmt.Sprint(want) {
		t.Errorf("opt-outs = %v, want %v", have, want)
	}
	for _, m := range got.Matches {
		if m.Line == 13 {
			return
		}
	}
	t.Error("an ast-grep-ignore comment suppressed a match")
}
