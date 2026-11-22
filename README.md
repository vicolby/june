# june

[![ci](https://github.com/vicolby/june/actions/workflows/ci.yml/badge.svg)](https://github.com/vicolby/june/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/vicolby/june)](https://github.com/vicolby/june/releases)

**Deterministic enforcement of code-guideline rules on the lines a change touches.**

june turns the guideline rules a tool can decide into CI failures that an AI agent fixes like a ruff or mypy error.

- **Only touched lines fail.** Old violations never fail a build: no cleanup, no baseline file.
- **Same verdict everywhere.** No network, no model: the same input gives the same result locally, in GitHub Actions and in GitLab CI.
- **Rules are verified.** A rule whose passing and failing examples misbehave is refused.

Version 1 checks Python only.

## Install

june needs `ast-grep` on `PATH`. It never loads the bundled rules on its own: pass them with `--rules` or copy them to `.june/rules`.

**Homebrew.** Installs `june`, `ast-grep` and the rules in `$(brew --prefix)/share/june/rules`.

```sh
brew install vicolby/tap/june
```

**Install script.** Installs the release binary and rules after a sha256 check. In CI, pin the script and `JUNE_VERSION` to the same [release](https://github.com/vicolby/june/releases) that lists `install.sh` among its assets (replace `X.Y.Z`):

```sh
curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z sh
```

| Variable | Meaning |
| --- | --- |
| `JUNE_VERSION` | Release, with or without `v`. Default: latest. Always set it in CI. |
| `JUNE_INSTALL_DIR` | Binary dir. Default: `/usr/local/bin` as root, else `$HOME/.local/bin`. |
| `JUNE_RULES_DIR` | Rules dir, replaced if present. Default: `/usr/local/share/june/rules` as root, else `$HOME/.local/share/june/rules`. |
| `JUNE_OS`, `JUNE_ARCH` | Override platform: `linux`/`darwin`, `amd64`/`arm64`. |
| `JUNE_INSTALL_AST_GREP` | `1` also runs `npm install --global @ast-grep/cli`. |
| `JUNE_RELEASES_URL` | Default: `https://github.com/vicolby/june/releases`. |

The script is plain POSIX [`install.sh`](install.sh); read it before piping to `sh`.

**Release archive.** Each release attaches `june_<version>_<os>_<arch>.tar.gz` (`darwin`/`linux`, `amd64`/`arm64`), `checksums.txt` and `install.sh`. The archive has no top-level directory: it holds `june` and `rules/`, not `ast-grep`.

## Usage

```text
june [--legacy] [--rules DIR]... [--base REF | --changed-lines FILE]
june verify [--rules DIR]...
june --version
```

| Flag | Meaning |
| --- | --- |
| `--rules DIR` | Rules directory, repeatable. Default: `./.june/rules`. |
| `--base REF` | Judge lines changed since the merge base of `REF` and `HEAD`. |
| `--changed-lines FILE` | Judge lines listed in `FILE`, one `path:7` or `path:7-12` per line; `-` reads stdin. |
| `--legacy` | Also list violations on untouched lines, as information only. |

```sh
june --rules "$(brew --prefix)/share/june/rules/python" --base main
```

Without `--base` or `--changed-lines`, june infers the base, first hit wins: pre-commit refs, pull request or merge request base in CI, pre-commit staged diff, upstream branch, remote default branch. The summary names the one used. When the touched lines cannot be determined (a shallow clone, say) june exits 2 and names the fix.

## Rules

Bundled [ast-grep](https://ast-grep.github.io) rules; only an error fails the check.

`PY012` mutable default argument, `PY013` bare `except`, `PY014` comparison to `None` with `==` or `!=`, `PY015` `except` that only passes, `PY016` `assert` on a tuple.

Every rule has `valid` and `invalid` examples in `rule-tests/`. A normal run verifies the rules it loads first; run `june verify --rules DIR` in the CI of a repo that keeps its own rules.

### Opt-outs

Opt out of one rule on a line with a reason:

```python
def tag(items, cache={}):  # june: ignore[PY012] cache is shared on purpose
```

Name exactly one rule and give a nonblank reason after the `]`. A malformed comment suppresses nothing and says why. Every opt-out a change adds is printed and counted in the summary.

## CI

### GitHub Actions

On a pull request june judges lines changed since the PR base and annotates the diff. Fetch full history. Homebrew on Ubuntu images is off `PATH`:

```yaml
on: pull_request
jobs:
  june:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - run: echo "/home/linuxbrew/.linuxbrew/bin" >> "$GITHUB_PATH"
      - run: brew install vicolby/tap/june
      - run: june --rules "$(brew --prefix)/share/june/rules/python"
```

On `push` there is no PR base, so june exits 2: pass `--base origin/main` with `fetch-depth: 0`, or run on `pull_request`.

### GitLab CI

In a merge request pipeline june judges the lines the MR changed and writes a Code Quality report (error `major`, warning `minor`, opt-out `info`). Use full clone depth; a shallow clone exits 2. `X.Y.Z` is a pinned release as above.

```yaml
june:
  image: node:22  # any image with git, curl and npm
  variables:
    GIT_DEPTH: 0
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  before_script:
    - curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z JUNE_INSTALL_AST_GREP=1 sh
  script:
    - june --rules .june/rules
  artifacts:
    when: always
    reports:
      codequality: gl-code-quality-report.json
```

The MR widget compares against the latest report on the target branch, so also run the job on the default branch: add `- if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH` to `rules` and use `--base HEAD~1` there, since a branch pipeline has no MR base.

### Pre-commit

```yaml
repos:
  - repo: https://github.com/vicolby/june
    rev: v0.3.0  # latest release tag; v0.3.0 is the first with the hook
    hooks:
      - id: june
        args: [--rules, PREFIX/share/june/rules/python]  # omit to use .june/rules
```

Install june with Homebrew. `PREFIX` is the literal `brew --prefix` output; pre-commit does not expand `$(...)`. The hook runs without filenames when Python files are involved and judges the staged change against HEAD. Under `--from-ref A --to-ref B`, or a pre-push that sets them, it judges the lines that range adds. A first push of a history with a root commit sets no refs, so pre-push judges nothing; CI covers it.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | No error-severity finding on a touched line. |
| `1` | An error-severity finding on a touched line with no valid opt-out. |
| `2` | No verdict: bad usage, bad rules directory, shallow clone, no base, missing `ast-grep`. Output: `june: error: <cause>. Fix: <instruction>`. |

Warnings, `--legacy` lines and opt-outs never change the exit code.

## More

- [VISION.md](VISION.md): purpose, scope, and the planned advisory LLM review (not built).
- [ARCHITECTURE.md](ARCHITECTURE.md): modules, guarantees, design.
