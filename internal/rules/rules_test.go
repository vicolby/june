package rules_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLoadCountsRulesAndExamples(t *testing.T) {
	rs, _ := rules.Load(context.Background(), fixture("good"))
	if rs == nil {
		t.Fatal("Load returned no rule set")
	}
	if rs.Rules() != 1 || rs.Examples() != 2 {
		t.Errorf("Rules, Examples = %d, %d, want 1, 2", rs.Rules(), rs.Examples())
	}
}

// Every misbehaving example is its own fault, so an author sees all of them at once.
func TestLoadNamesEveryMisbehavingExample(t *testing.T) {
	_, faults := rules.Load(context.Background(), fixture("bad_example"))
	if len(faults) != 2 {
		t.Fatalf("faults = %v, want 2", faults)
	}
	want := []string{
		`rule LST01 valid example "x = [1]" is reported by the rule`,
		`rule LST01 invalid example "x = (1,)" is not reported by the rule`,
	}
	for i, f := range faults {
		if f.Path != "rule-tests/LST01-test.yml" || f.Cause != want[i] {
			t.Errorf("fault %d = %+v, want path rule-tests/LST01-test.yml and cause %q", i, f, want[i])
		}
	}
}

func TestLoadRefusesUnverifiedRules(t *testing.T) {
	tests := []struct {
		dir       string
		wantPath  string
		wantCause string
	}{
		{"bad_example", "rule-tests/LST01-test.yml", "valid example \"x = [1]\" is reported"},
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

	got, err := scanFiles(context.Background(), rs, py, txt)
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
	got, err := scanFiles(context.Background(), rs)
	if err != nil || len(got.Matches) != 0 {
		t.Errorf("Scan(nil) = %+v, %v, want nothing", got, err)
	}
	got, err = scanFiles(context.Background(), rs, filepath.Join(dir, "notes.md"))
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
	if _, err := scanFiles(ctx, rs, py); err == nil {
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
			got, err := scanFiles(context.Background(), rs, good, bad)
			if err == nil || len(got.Matches) != 0 {
				t.Fatalf("Scan = %+v, %v, want an error and no partial matches", got, err)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("error exposes an absolute path: %v", err)
			}
			var notText *rules.NotTextError
			if isText := name == "invalid UTF-8" || name == "NUL"; errors.As(err, &notText) != isText || (isText && notText.File != bad) {
				t.Errorf("error = %#v, want a NotTextError naming %s only for non-text input", err, bad)
			}
		})
	}
}

func TestScanLargeFile(t *testing.T) {
	rs := loadGood(t)
	py := filepath.Join(t.TempDir(), "large.py")
	write(t, py, "#"+strings.Repeat(" ", 4*1024*1024)+"\nx = []\n")
	got, err := scanFiles(context.Background(), rs, py)
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
	got, err := scanFiles(context.Background(), rs, py)
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
	got, err := scanFiles(context.Background(), rs, py)
	if err == nil || len(got.Matches) != 0 || strings.Contains(err.Error(), dir) {
		t.Fatalf("Scan = %+v, %v, want a refusal without absolute paths", got, err)
	}
}

func TestLoadQuotesEachExampleWhenAnotherRulePasses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "AAA.yml"), "id: AAA\nlanguage: python\nseverity: warning\nmessage: 'Fix: use a tuple'\nrule:\n  kind: list\n")
	write(t, filepath.Join(dir, "rule-tests", "AAA-test.yml"), "id: AAA\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n")
	write(t, filepath.Join(dir, "ZZZ.yml"), "id: ZZZ\nlanguage: python\nseverity: error\nmessage: 'Fix: use a tuple'\nrule:\n  kind: list\n")
	write(t, filepath.Join(dir, "rule-tests", "ZZZ-test.yml"), "id: ZZZ\nvalid:\n  - \"x = [1]\"\ninvalid:\n  - \"x = (1,)\"\n")

	rs, faults := rules.Load(context.Background(), dir)
	if rs != nil {
		t.Fatal("Load returned a rule set")
	}
	want := []string{
		`rule ZZZ valid example "x = [1]" is reported by the rule`,
		`rule ZZZ invalid example "x = (1,)" is not reported by the rule`,
	}
	if len(faults) != len(want) {
		t.Fatalf("faults = %+v, want %d", faults, len(want))
	}
	for i, f := range faults {
		if f.Path != "rule-tests/ZZZ-test.yml" || f.Cause != want[i] {
			t.Errorf("fault %d = %+v, want path rule-tests/ZZZ-test.yml and cause %q", i, f, want[i])
		}
	}
}

func TestLoadExecutesSeverityOffRules(t *testing.T) {
	const draft = "id: DRAFT\nlanguage: python\nseverity: off\nmessage: 'Fix: use a tuple'\nrule:\n  kind: list\n"
	const py = "id: PY012\nlanguage: python\nseverity: error\nmessage: 'Fix: use a tuple'\nrule:\n  kind: list\n"
	const pyExamples = "id: PY012\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n"
	goodDraft := "id: DRAFT\nvalid:\n  - \"x = (1,)\"\ninvalid:\n  - \"x = [1]\"\n"
	badDraft := "id: DRAFT\nvalid:\n  - \"x = [1]\"\ninvalid:\n  - \"x = (1,)\"\n"

	t.Run("failing example", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "PY012.yml"), py)
		write(t, filepath.Join(dir, "rule-tests", "PY012-test.yml"), pyExamples)
		write(t, filepath.Join(dir, "DRAFT.yml"), draft)
		write(t, filepath.Join(dir, "rule-tests", "DRAFT-test.yml"), badDraft)

		rs, faults := rules.Load(context.Background(), dir)
		if rs != nil {
			t.Fatal("Load returned a rule set")
		}
		want := []string{
			`rule DRAFT valid example "x = [1]" is reported by the rule`,
			`rule DRAFT invalid example "x = (1,)" is not reported by the rule`,
		}
		if len(faults) != len(want) {
			t.Fatalf("faults = %+v, want %d", faults, len(want))
		}
		for i, f := range faults {
			if f.Path != "rule-tests/DRAFT-test.yml" || f.Cause != want[i] {
				t.Errorf("fault %d = %+v, want path rule-tests/DRAFT-test.yml and cause %q", i, f, want[i])
			}
		}
	})

	t.Run("passing example", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "PY012.yml"), py)
		write(t, filepath.Join(dir, "rule-tests", "PY012-test.yml"), pyExamples)
		write(t, filepath.Join(dir, "DRAFT.yml"), draft)
		write(t, filepath.Join(dir, "rule-tests", "DRAFT-test.yml"), goodDraft)

		rs, faults := rules.Load(context.Background(), dir)
		if len(faults) != 0 || rs == nil || rs.Rules() != 2 || rs.Examples() != 4 {
			t.Fatalf("Load = %v, %v, want 2 rules and 4 examples", rs, faults)
		}
	})

	t.Run("passing alone", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "DRAFT.yml"), draft)
		write(t, filepath.Join(dir, "rule-tests", "DRAFT-test.yml"), goodDraft)

		rs, faults := rules.Load(context.Background(), dir)
		if len(faults) != 0 || rs == nil || rs.Rules() != 1 || rs.Examples() != 2 {
			t.Fatalf("Load = %v, %v, want 1 rule and 2 examples", rs, faults)
		}
	})
}

func TestRunIgnoresProjectConfigInWorkingDirectory(t *testing.T) {
	rs := loadGood(t)
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "sgconfig.yml"), "ruleDirs: [rules]\nutilDirs: [missing]\n")
	t.Chdir(cwd)

	py := filepath.Join(t.TempDir(), "a.py")
	write(t, py, "x = [1]  # ast-grep-ignore\ny = [2]\n")
	got, err := scanFiles(context.Background(), rs, py)
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
	got, err := scanFiles(context.Background(), rs, py)
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

func TestLoadMergesDirectories(t *testing.T) {
	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(other, "T01.yml"), "id: T01\nlanguage: python\nseverity: error\nmessage: No dicts.\nrule:\n  kind: dictionary\n")
	write(t, filepath.Join(other, "rule-tests", "T01-test.yml"), "id: T01\nvalid:\n  - \"x = 1\"\ninvalid:\n  - \"x = {}\"\n")
	rs, faults := rules.Load(context.Background(), fixture("good"), other)
	if rs == nil || len(faults) != 0 {
		t.Fatalf("Load = %v, %v, want a rule set", rs, faults)
	}
	if rs.Rules() != 2 || rs.Examples() != 4 {
		t.Errorf("Rules, Examples = %d, %d, want 2, 4", rs.Rules(), rs.Examples())
	}
}

func TestLoadDuplicateIDAcrossDirectoriesNamesBothPlaces(t *testing.T) {
	_, faults := rules.Load(context.Background(), fixture("good"), fixture("good"))
	if len(faults) != 1 {
		t.Fatalf("faults = %v, want 1", faults)
	}
	f := faults[0]
	if f.Dir != 1 || f.Path != "LST01.yml" || f.Also == nil || *f.Also != (rules.Place{Dir: 0, Path: "LST01.yml"}) || !strings.Contains(f.Cause, "duplicated") {
		t.Errorf("fault = %+v, want dir 1 LST01.yml duplicated with Also dir 0 LST01.yml", f)
	}
}

func TestLoadCaseVariantIDIsADuplicate(t *testing.T) {
	one := t.TempDir()
	two := t.TempDir()
	// Foo's valid example is a list the rule matches, so admitting it unverified would hide a failing example.
	writeRule(t, one, "Foo.yml", "Foo", "x = [1]")
	writeRule(t, two, "foo.yml", "foo", "x = (1,)")
	rs, faults := rules.Load(context.Background(), one, two)
	if rs != nil || len(faults) != 1 {
		t.Fatalf("Load = %v, %v, want one duplicate fault and no rule set", rs, faults)
	}
	f := faults[0]
	if f.Dir != 1 || f.Path != "foo.yml" || f.Also == nil || *f.Also != (rules.Place{Dir: 0, Path: "Foo.yml"}) || !strings.Contains(f.Cause, "duplicated") {
		t.Errorf("fault = %+v, want dir 1 foo.yml duplicated with Also dir 0 Foo.yml", f)
	}
}

func TestLoadRefusesIDsThatShareAScratchFilename(t *testing.T) {
	const nfc = "Caf\u00e9"
	const nfd = "Cafe\u0301"
	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, nfc), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(probe, nfd), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(probe, nfc))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "2" {
		t.Skip("this filesystem stores the two names as different files")
	}
	one := t.TempDir()
	two := t.TempDir()
	writeRule(t, one, "rule.yml", nfc, "x = [1]")
	writeRule(t, two, "rule.yml", nfd, "x = (1,)")
	rs, faults := rules.Load(context.Background(), one, two)
	if rs != nil || len(faults) != 1 {
		t.Fatalf("Load = %v, %v, want one duplicate fault and no rule set", rs, faults)
	}
	f := faults[0]
	if f.Dir != 1 || f.Path != "rule.yml" || f.Also == nil || *f.Also != (rules.Place{Dir: 0, Path: "rule.yml"}) || !strings.Contains(f.Cause, "duplicated") || !strings.Contains(f.Cause, nfd) {
		t.Errorf("fault = %+v, want dir 1 rule.yml duplicated with Also dir 0 rule.yml", f)
	}
}

func writeRule(t *testing.T, dir, file, id, valid string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, file), "id: "+id+"\nlanguage: python\nseverity: error\nmessage: No lists.\nrule:\n  kind: list\n")
	write(t, filepath.Join(dir, "rule-tests", id+"-test.yml"), "id: "+id+"\nvalid:\n  - \""+valid+"\"\ninvalid:\n  - \"x = [1]\"\n")
}

func TestLoadReportsEveryBadDirectory(t *testing.T) {
	_, faults := rules.Load(context.Background(), fixture("missing"), fixture("good"), fixture("empty"))
	if len(faults) != 2 || faults[0].Dir != 0 || faults[1].Dir != 2 {
		t.Fatalf("faults = %+v, want one each for dirs 0 and 2", faults)
	}
}

func TestLoadWithoutDirectoriesIsAFault(t *testing.T) {
	if rs, faults := rules.Load(context.Background()); rs != nil || len(faults) != 1 {
		t.Fatalf("Load = %v, %v, want one fault", rs, faults)
	}
}

// scanFiles scans files that share one directory, and reports their matches under the absolute paths it was given,
// so a test reads like the files it wrote. A non-text error names that same path.
func scanFiles(ctx context.Context, rs *rules.RuleSet, files ...string) (rules.Scanned, error) {
	if len(files) == 0 {
		return rs.Scan(ctx, "", nil)
	}
	root := filepath.Dir(files[0])
	rel := make([]string, len(files))
	for i, f := range files {
		rel[i] = filepath.Base(f)
	}
	got, err := rs.Scan(ctx, root, rel)
	for i := range got.Matches {
		got.Matches[i].File = filepath.Join(root, got.Matches[i].File)
	}
	for i := range got.OptOuts {
		got.OptOuts[i].File = filepath.Join(root, got.OptOuts[i].File)
	}
	var notText *rules.NotTextError
	if errors.As(err, &notText) {
		for i, name := range rel {
			if name == notText.File {
				return got, &rules.NotTextError{File: files[i]}
			}
		}
	}
	return got, err
}

// manyRules writes n rules, each matching its own call target_NNN() and padded to size bytes,
// with the sidecar examples Load requires.
func manyRules(t *testing.T, n, size int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("BIG%03d", i)
		head := fmt.Sprintf("id: %s\nlanguage: python\nseverity: error\nrule:\n  pattern: target_%03d()\nmessage: 'Fix: ", id, i)
		pad := strings.Repeat("x", max(0, size-len(head)-1))
		write(t, filepath.Join(dir, id+".yml"), head+pad+"'\n")
		write(t, filepath.Join(dir, "rule-tests", id+"-test.yml"),
			fmt.Sprintf("id: %s\nvalid: ['other()']\ninvalid: ['target_%03d()']\n", id, i))
	}
	return dir
}

// Linux caps one argv string at 128 KiB and macOS the whole argv at 1 MiB; a rule set must not depend on either.
func TestScanRuleSetsBeyondArgvLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rules, size int
	}{
		{"270 rules just over the Linux string limit", 270, 500},
		{"300 rules over the macOS total limit", 300, 4500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs, faults := rules.Load(context.Background(), manyRules(t, tc.rules, tc.size))
			if rs == nil {
				t.Fatalf("Load: %v", faults)
			}
			dir := t.TempDir()
			write(t, filepath.Join(dir, "app.py"), "target_007()\nother()\ntarget_269()\n")
			got, err := rs.Scan(context.Background(), dir, []string{"app.py"})
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(got.Matches) != 2 || got.Matches[0].Rule != "BIG007" || got.Matches[0].Line != 1 || got.Matches[1].Rule != "BIG269" || got.Matches[1].Line != 3 {
				t.Fatalf("matches = %+v, want BIG007 on line 1 and BIG269 on line 3", got.Matches)
			}
		})
	}
}

// A failed scan says why in ast-grep's own first line, without the scratch path it ran in.
func TestScanErrorCarriesAstGrepReason(t *testing.T) {
	real, err := exec.LookPath("ast-grep")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = test ] && exec " + real + " \"$@\"\necho \"Error: boom at $PWD/rules\" >&2\necho 'second line' >&2\nexit 2\n"
	write(t, filepath.Join(bin, "ast-grep"), script)
	if err := os.Chmod(filepath.Join(bin, "ast-grep"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	rs := loadGood(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "app.py"), "x = []\n")
	_, err = rs.Scan(context.Background(), dir, []string{"app.py"})
	if err == nil || !strings.Contains(err.Error(), "ast-grep could not complete the scan: Error: boom at") || strings.Contains(err.Error(), "second line") {
		t.Fatalf("Scan error = %v, want ast-grep's first stderr line only", err)
	}
	if strings.Contains(err.Error(), os.TempDir()) || strings.Contains(err.Error(), dir) {
		t.Errorf("error exposes an absolute path: %v", err)
	}
}

// One scan serves any number of files: it must stay one process and one tree walk, not one process per file.
func TestScanThreeThousandFilesWithinBudget(t *testing.T) {
	rs := loadGood(t)
	dir := t.TempDir()
	files := make([]string, 3000)
	for i := range files {
		files[i] = fmt.Sprintf("pkg%d/mod%d.py", i%50, i)
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(files[i])), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, files[i]), "x = []\ny = 1  # june: ignore[LST01] kept\n")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := rs.Scan(ctx, dir, files)
	if err != nil {
		t.Fatalf("scanning %d files did not finish within the 5s budget: %v", len(files), err)
	}
	if len(got.Matches) != len(files) || len(got.OptOuts) != len(files) {
		t.Fatalf("got %d matches and %d opt-outs, want %d of each", len(got.Matches), len(got.OptOuts), len(files))
	}
	for i, m := range got.Matches {
		if m.File != files[i] {
			t.Fatalf("match %d is in %s, want results in the order files were given (%s)", i, m.File, files[i])
		}
	}
}

// A file keeps its identity however its path looks to ast-grep: dot directories are scanned, a path given
// twice is reported twice, and a name that is not UTF-8 cannot be lost or mistaken for another file.
func TestScanStagesEveryPathFaithfully(t *testing.T) {
	rs := loadGood(t)
	dir := t.TempDir()
	names := []string{".hidden/.deeper/a.py", "sp ace/q\"uote.py", "plain.py", "plain.py"}
	odd := "bad\xffname.py"
	if err := os.WriteFile(filepath.Join(dir, odd), []byte("x = []\n"), 0o644); err == nil {
		names = append(names, odd)
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(n)), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, n), "x = []\n")
	}
	got, err := rs.Scan(context.Background(), dir, names)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, m := range got.Matches {
		have = append(have, m.File)
	}
	if strings.Join(have, "|") != strings.Join(names, "|") {
		t.Fatalf("matches in %q, want one per given path in order %q", have, names)
	}
}

func TestScanRefusesPathsOutsideRoot(t *testing.T) {
	rs := loadGood(t)
	outer := t.TempDir()
	root := filepath.Join(outer, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outer, "escape.py"), "x = []\n")
	for _, f := range []string{"../escape.py", filepath.Join(outer, "escape.py")} {
		if got, err := rs.Scan(context.Background(), root, []string{f}); err == nil || len(got.Matches) != 0 {
			t.Errorf("Scan(%q) = %+v, %v, want a refusal", f, got, err)
		}
	}
}

// Path filters inside a rule have never applied, and a scan that stages real paths must not start applying them:
// honouring `files` and `ignores` is a decision of its own, so a rule keeps matching every file it is given.
func TestScanDoesNotApplyRulePathFilters(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rule-tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "PATH01.yml"), "id: PATH01\nlanguage: python\nseverity: error\nmessage: 'Fix: no lists'\nfiles: ['**/only/**']\nignores: ['**/legacy/**']\nrule:\n  kind: list\n")
	write(t, filepath.Join(dir, "rule-tests", "PATH01-test.yml"), "id: PATH01\nvalid: ['x = 1']\ninvalid: ['x = [1]']\n")
	rs, faults := rules.Load(context.Background(), dir)
	if rs == nil {
		t.Fatalf("Load: %v", faults)
	}
	src := t.TempDir()
	files := []string{"legacy/b.py", "elsewhere/c.py", "only/d.py"}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(src, f), "x = [1]\n")
	}
	got, err := rs.Scan(context.Background(), src, files)
	if err != nil || len(got.Matches) != len(files) {
		t.Fatalf("Scan = %+v, %v, want a match in each of %v", got.Matches, err, files)
	}
}
