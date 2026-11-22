package rules

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// MinVersion is the oldest ast-grep june accepts: the oldest release the whole suite passes on.
// Before it, ast-grep test accepts a rule with an unknown key, so verify would pass a rule that is not what its author wrote.
const MinVersion = "0.51.1"

// PinnedVersion is the ast-grep that install.sh and the docs recommend; it is also the version in .ast-grep-version.
const PinnedVersion = "0.51.3"

var versionLine = regexp.MustCompile(`^ast-grep (\d+)\.(\d+)\.(\d+)$`)

// Version runs bin --version and returns the version, such as "0.51.3".
func Version(ctx context.Context, bin string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version failed", astGrepName)
	}
	text := strings.TrimSpace(string(out))
	m := versionLine.FindStringSubmatch(text)
	if m == nil {
		return "", fmt.Errorf("%s --version printed %q", astGrepName, oneLine(text))
	}
	return strings.TrimPrefix(text, astGrepName+" "), nil
}

// Supported reports whether version, as returned by Version, is at least MinVersion.
func Supported(version string) bool {
	return compare(version, MinVersion) >= 0
}

// compare orders two X.Y.Z releases.
func compare(a, b string) int {
	x, xok := core(a)
	y, yok := core(b)
	if !xok || !yok {
		return -1
	}
	for i := range x {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func core(v string) ([3]int, bool) {
	var n [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return n, false
	}
	for i, part := range parts {
		if part == "" {
			return n, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return n, false
			}
		}
		n[i], _ = strconv.Atoi(part)
	}
	return n, true
}

// versionFault checks the ast-grep at bin and returns its version, or the fault that refuses it.
func versionFault(ctx context.Context, bin string) (string, *Fault) {
	v, err := Version(ctx, bin)
	if err != nil {
		return "", &Fault{Cause: "the ast-grep version cannot be read (" + err.Error() + ")", Fix: astGrepFixVersion}
	}
	if !Supported(v) {
		return v, &Fault{Cause: fmt.Sprintf("ast-grep %s is older than the oldest supported version %s", v, MinVersion), Fix: astGrepFixVersion}
	}
	return v, nil
}

const astGrepFixVersion = "install ast-grep " + PinnedVersion + " or newer, for example npm install --global @ast-grep/cli@" + PinnedVersion
