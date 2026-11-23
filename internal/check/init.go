package check

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vicolby/june/internal/diag"
	"github.com/vicolby/june/internal/rules"
)

// InitRequest asks for a project to be set up.
type InitRequest struct {
	// CI selects github or gitlab; empty means github.
	CI string
	// Version is the june release the snippets pin, as stamped into the binary; anything but X.Y.Z leaves a placeholder.
	Version string
}

const starterRuleID = "MY001"

const configStarter = `# june configuration. Every key is optional. Paths and globs are relative to the repository root.
#
# rules:             # rules directories, default .june/rules
#   - .june/rules
# base: origin/main  # judge against this ref when no CI event or pre-commit scope names one
# exclude:           # path globs kept out of the check
#   - vendor
`

const githubWorkflow = `name: june
on:
  pull_request:
  merge_group:
jobs:
  june:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: vicolby/june@v%[1]s
`

const gitlabInclude = `# Include from .gitlab-ci.yml:
#   include:
#     - local: .june/gitlab-ci.yml
june:
  image: node:22  # any image with git, curl and npm
  variables:
    GIT_DEPTH: 0
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  before_script:
    - curl -fsSL https://github.com/vicolby/june/releases/download/v%[1]s/install.sh | JUNE_VERSION=%[1]s JUNE_INSTALL_AST_GREP=1 sh
  script:
    - june
  artifacts:
    when: always
    reports:
      codequality: gl-code-quality-report.json
`

const preCommitConfig = `repos:
  - repo: https://github.com/vicolby/june
    rev: v%[1]s
    hooks:
      - id: june
`

var releaseVersion = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// Init sets up the repository with a starter rule, a config, one CI snippet and the pre-commit hook config.
// It is idempotent: files that exist are kept. Exit 2 reports a file that cannot be written.
func Init(ctx context.Context, env Env, req InitRequest) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	out := Request{Format: diag.Text}
	provider := req.CI
	if provider == "" {
		provider = "github"
	}
	if provider != "github" && provider != "gitlab" {
		return refuse(out, fmt.Sprintf("unknown CI provider %q. Fix: use june init --ci github or june init --ci gitlab", provider)), nil
	}
	root, prefix := repoLocation(ctx, env)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if root == "" {
		return refuse(out, "june init requires a git repository. Fix: run june init inside a git work tree"), nil
	}
	version, pinned := "X.Y.Z", false
	if m := releaseVersion.FindStringSubmatch(req.Version); m != nil {
		version, pinned = m[1], true
	}

	var b strings.Builder
	// show names a path the way the caller can reach it from the working directory.
	show := func(p string) string {
		rel, err := filepath.Rel(root, p)
		if err == nil && prefix != "" {
			rel, err = filepath.Rel(prefix, rel)
		}
		if err != nil {
			return p
		}
		return filepath.ToSlash(rel)
	}

	starterCreated := false
	rulesDir := filepath.Join(root, defaultRules)
	if entries, err := os.ReadDir(rulesDir); err == nil && len(entries) > 0 {
		fmt.Fprintf(&b, "kept %s (exists)\n", show(rulesDir))
	} else {
		made, fault := rules.Scaffold(rulesDir, starterRuleID)
		if fault != nil {
			return refuseWith(out, faultDiagnostics(root, []string{rulesDir}, []rules.Fault{*fault})...), nil
		}
		starterCreated = true
		for _, m := range made {
			fmt.Fprintf(&b, "created %s\n", show(filepath.Join(rulesDir, m)))
		}
	}

	ciPath, ciBody := ".github/workflows/june.yml", githubWorkflow
	if provider == "gitlab" {
		ciPath, ciBody = ".june/gitlab-ci.yml", gitlabInclude
	}
	files := []struct{ rel, body string }{
		{defaultConfig, configStarter},
		{ciPath, fmt.Sprintf(ciBody, version)},
		{".pre-commit-config.yaml", fmt.Sprintf(preCommitConfig, version)},
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.rel))
		err := writeFile(p, []byte(f.body))
		switch {
		case errors.Is(err, fs.ErrExist):
			fmt.Fprintf(&b, "kept %s (exists)\n", show(p))
		case err != nil:
			return refuse(out, fmt.Sprintf("%s cannot be written (%v). Fix: make the directory writable", show(p), err)), nil
		default:
			fmt.Fprintf(&b, "created %s\n", show(p))
		}
	}
	if !pinned {
		b.WriteString("note: this build has no release version, so the snippets pin vX.Y.Z. Fix: replace X.Y.Z with the june release you use\n")
	}
	if starterCreated {
		fmt.Fprintf(&b, "next: edit the starter rule, then run june verify --rules %s --rule %s\n", shellQuote(show(rulesDir)), starterRuleID)
	} else {
		b.WriteString("next: run june verify to verify the preserved rules\n")
	}
	return Result{Out: []byte(b.String())}, nil
}

// writeFile creates path and its directories. An existing file is fs.ErrExist.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
