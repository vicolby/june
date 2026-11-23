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
	// Force overwrites the CI and hook files that exist. Rules are never overwritten.
	Force bool
	// Version is the june release the snippets pin, as stamped into the binary; anything but X.Y.Z leaves a placeholder.
	Version string
}

const starterRuleID = "MY001"

const configStarter = `# june configuration. Every key is optional. Paths and globs are relative to the repository root.
#
# rules:             # rules directories, default .june/rules
#   - .june/rules
# base: origin/main  # judge against this ref when no CI event or pre-commit ref names one
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

// Init sets up the repository june runs in (the working directory outside git): the rules directory with a starter rule
// that passes verification, a commented config, a GitHub Actions workflow, a GitLab CI include and the pre-commit hook config.
// It is idempotent: a file that exists is kept, unless Force asks for the CI and hook files to be replaced.
// An existing rules directory is never touched. Exit 2 reports a file that cannot be written.
func Init(ctx context.Context, env Env, req InitRequest) (Result, error) {
	if env.Dir == "" {
		return Result{}, errors.New("check: Env.Dir is empty")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	out := Request{Format: diag.Text}
	root, prefix := repoLocation(ctx, env)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if root == "" {
		root = env.Dir
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

	rulesDir := filepath.Join(root, defaultRules)
	if entries, err := os.ReadDir(rulesDir); err == nil && len(entries) > 0 {
		fmt.Fprintf(&b, "kept %s (exists)\n", show(rulesDir))
	} else {
		made, fault := rules.Scaffold(rulesDir, starterRuleID)
		if fault != nil {
			return refuseWith(out, faultDiagnostics(root, []string{rulesDir}, []rules.Fault{*fault})...), nil
		}
		for _, m := range made {
			fmt.Fprintf(&b, "created %s\n", show(filepath.Join(rulesDir, m)))
		}
	}

	files := []struct {
		rel, body string
		replace   bool // Force may replace it: it is generated snippet, not something the user wrote
	}{
		{defaultConfig, configStarter, false},
		{".github/workflows/june.yml", fmt.Sprintf(githubWorkflow, version), true},
		{".june/gitlab-ci.yml", fmt.Sprintf(gitlabInclude, version), true},
		{".pre-commit-config.yaml", fmt.Sprintf(preCommitConfig, version), true},
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.rel))
		err := writeFile(p, []byte(f.body), req.Force && f.replace)
		switch {
		case errors.Is(err, fs.ErrExist):
			hint := "exists"
			if f.replace {
				hint += "; --force replaces it"
			}
			fmt.Fprintf(&b, "kept %s (%s)\n", show(p), hint)
		case err != nil:
			return refuse(out, fmt.Sprintf("%s cannot be written (%v). Fix: make the directory writable", show(p), err)), nil
		default:
			fmt.Fprintf(&b, "created %s\n", show(p))
		}
	}
	if !pinned {
		b.WriteString("note: this build has no release version, so the snippets pin vX.Y.Z. Fix: replace X.Y.Z with the june release you use\n")
	}
	fmt.Fprintf(&b, "next: edit the starter rule, then run june verify --rule %s\n", starterRuleID)
	return Result{Out: []byte(b.String())}, nil
}

// writeFile creates path and its directories. An existing file is fs.ErrExist unless replace is set.
func writeFile(path string, data []byte, replace bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if replace {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
