package rules_test

import (
	"context"
	"os"
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
