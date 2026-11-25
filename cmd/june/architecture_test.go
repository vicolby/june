package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// goList runs go list in the module and returns its output lines.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

// Only june review opens a connection. The packages that decide a check, and the review engine that decides what a
// model is asked, import no network package, so the README's "no network on the check path" promise is a fact the
// build enforces.
func TestOnlyTheOpenAIAdapterReachesTheNetwork(t *testing.T) {
	network := map[string]bool{"net": true, "net/http": true, "crypto/tls": true}
	for _, pkg := range []string{"check", "rules", "diag", "review"} {
		for _, dep := range goList(t, "-deps", "./internal/"+pkg) {
			if network[dep] {
				t.Errorf("internal/%s depends on %s: only internal/openai may reach the network", pkg, dep)
			}
		}
	}
	deps := goList(t, "-deps", "./internal/openai")
	found := false
	for _, dep := range deps {
		found = found || dep == "net/http"
	}
	if !found {
		t.Errorf("internal/openai no longer imports net/http, so this test checks nothing")
	}
}

// The adapter is the one place that imports net/http, and only the command wires it in.
func TestOnlyTheAdapterImportsNetHTTP(t *testing.T) {
	out := goList(t, "-f", `{{.ImportPath}}:{{range .Imports}}{{if eq . "net/http"}}http{{end}}{{end}}`, "./...")
	for _, l := range out {
		pkg, imp, _ := strings.Cut(l, ":")
		if imp == "http" && !strings.HasSuffix(pkg, "/internal/openai") {
			t.Errorf("%s imports net/http: only internal/openai may", pkg)
		}
	}
}
