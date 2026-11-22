package rules_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicolby/june/internal/rules"
)

// fakeASTGrep puts an ast-grep on PATH that runs script, and returns nothing else on PATH.
func fakeASTGrep(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ast-grep"), "#!/bin/sh\n"+script+"\n")
	if err := os.Chmod(filepath.Join(dir, "ast-grep"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestSupported(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{rules.MinVersion, true},
		{rules.PinnedVersion, true},
		{"0.51.0", false},
		{"0.45.3", false},
		{"0.9.99", false},
		{"0.51.10", true},
		{"0.52.0", true},
		{"1.0.0", true},
		{"0.51.1-rc.1", true},
		{"0.51.0-rc.1", false},
	}
	for _, tt := range tests {
		if got := rules.Supported(tt.version); got != tt.want {
			t.Errorf("Supported(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestPinnedVersionIsTestedAndSupported(t *testing.T) {
	if !rules.Supported(rules.PinnedVersion) {
		t.Errorf("PinnedVersion %s is below MinVersion %s", rules.PinnedVersion, rules.MinVersion)
	}
	file, err := os.ReadFile(filepath.Join("..", "..", ".ast-grep-version"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(file)); got != rules.PinnedVersion {
		t.Errorf(".ast-grep-version = %q, want PinnedVersion %q", got, rules.PinnedVersion)
	}
}

func TestVersionOfRealASTGrep(t *testing.T) {
	bin, err := exec.LookPath("ast-grep")
	if err != nil {
		t.Fatal(err)
	}
	v, err := rules.Version(context.Background(), bin)
	if err != nil || !rules.Supported(v) {
		t.Fatalf("Version = %q, %v, want a supported version", v, err)
	}
	rs := loadGood(t)
	if rs.Version() != v {
		t.Errorf("RuleSet.Version = %q, want %q", rs.Version(), v)
	}
}

func TestLoadRefusesOldASTGrep(t *testing.T) {
	fakeASTGrep(t, "echo 'ast-grep 0.45.3'")
	rs, faults := rules.Load(context.Background(), fixture("good"))
	if rs != nil || len(faults) != 1 {
		t.Fatalf("Load = %v, %v, want one fault", rs, faults)
	}
	f := faults[0]
	if !strings.Contains(f.Cause, "ast-grep 0.45.3 is older than the oldest supported version "+rules.MinVersion) || !strings.Contains(f.Fix, "@ast-grep/cli@"+rules.PinnedVersion) {
		t.Errorf("fault = %+v, want the version, the floor and an install fix", f)
	}
}

func TestLoadRefusesUnreadableVersion(t *testing.T) {
	for name, script := range map[string]string{
		"garbage": "echo 'something else'",
		"empty":   "true",
		"fails":   "exit 3",
	} {
		t.Run(name, func(t *testing.T) {
			fakeASTGrep(t, script)
			rs, faults := rules.Load(context.Background(), fixture("good"))
			if rs != nil || len(faults) != 1 || !strings.Contains(faults[0].Cause, "ast-grep version cannot be read") {
				t.Fatalf("Load = %v, %v, want one unreadable-version fault", rs, faults)
			}
		})
	}
}

func TestLoadAcceptsTheFloorExactly(t *testing.T) {
	real, err := exec.LookPath("ast-grep")
	if err != nil {
		t.Fatal(err)
	}
	// A wrapper that reports the floor but runs the real binary for everything else.
	fakeASTGrep(t, `if [ "$1" = "--version" ]; then echo 'ast-grep `+rules.MinVersion+`'; else exec `+real+` "$@"; fi`)
	rs, faults := rules.Load(context.Background(), fixture("good"))
	if rs == nil || len(faults) != 0 || rs.Version() != rules.MinVersion {
		t.Fatalf("Load = %v, %v, want a set at version %s", rs, faults, rules.MinVersion)
	}
}
