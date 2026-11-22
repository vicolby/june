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

june needs `ast-grep` 0.51.1 or newer on `PATH`, reporting a numeric `X.Y.Z` release; prerelease and other suffixes are refused. A check or `june verify` with an older or unreadable version exits 2 with the fix. 0.51.3 is the tested version, and `june --version` and a successful `june verify` print the one in use. june ships no rules: you supply your own with `--rules DIR` or in `.june/rules`.

**Homebrew.** Installs `june` and Homebrew's current `ast-grep`.

```sh
brew install vicolby/tap/june
```

**Install script.** Installs the release binary after a sha256 check. In CI, pin the script and `JUNE_VERSION` to the same [release](https://github.com/vicolby/june/releases) that lists `install.sh` among its assets (replace `X.Y.Z`):

```sh
curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z sh
```

| Variable | Meaning |
| --- | --- |
| `JUNE_VERSION` | Release, with or without `v`. Default: latest. Always set it in CI. |
| `JUNE_INSTALL_DIR` | Binary dir. Default: `/usr/local/bin` as root, else `$HOME/.local/bin`. |
| `JUNE_OS`, `JUNE_ARCH` | Override platform: `linux`/`darwin`, `amd64`/`arm64`. |
| `JUNE_INSTALL_AST_GREP` | `1` also installs the tested ast-grep version above with `npm install --global @ast-grep/cli@<version>`. |
| `JUNE_RELEASES_URL` | Default: `https://github.com/vicolby/june/releases`. |

The script is plain POSIX [`install.sh`](install.sh); read it before piping to `sh`.

**Release archive.** Each release attaches `june_<version>_<os>_<arch>.tar.gz` (`darwin`/`linux`, `amd64`/`arm64`), `checksums.txt` and `install.sh`. The archive has no top-level directory: it holds `june`, not `ast-grep`.

## Usage

```text
june [--format FORMAT] [--legacy] [--ignore-whitespace] [--rules DIR]... [--base REF | --changed-lines FILE]
june verify [--rules DIR]...
june --version
june --help | -h
```

| Flag | Meaning |
| --- | --- |
| `--rules DIR` | Rules directory, repeatable. Default: `./.june/rules`. |
| `--base REF` | Judge lines changed since the merge base of `REF` and `HEAD`. |
| `--changed-lines FILE` | Judge lines listed in `FILE`, one `path:7` or `path:7-12` per line; `-` reads stdin. |
| `--format FORMAT` | `text`, `json`, `sarif`, `github` or `gitlab`. Default: `github` under GitHub Actions, `gitlab` under GitLab CI, else `text`. |
| `--legacy` | Also list violations on untouched lines, as information only. |
| `--ignore-whitespace` | Do not count a line as touched when only its whitespace changed, like `git diff -w`. Strict by default. Not valid with `--changed-lines`. |
| `--help`, `-h` | Print usage and flags to stdout and exit 0. |

```sh
june --rules path/to/rules --base main
```

Without `--base` or `--changed-lines`, june infers the base, first hit wins: pre-commit refs, pull request or merge request base in CI, pre-commit staged diff, upstream branch, remote default branch. The summary names the one used. When the touched lines cannot be determined (a shallow clone, say) june exits 2 and names the fix.

### Source encoding

june defaults to UTF-8 and honors a PEP 263 coding declaration (e.g. `# -*- coding: latin-1 -*-`) on the first line, or the second when the first is blank or comment-only. Supported encodings are `utf-8`, `latin-1`, `ascii` and `cp1252`; transcoding preserves line numbers.

Unsupported declarations, invalid bytes for the selected encoding, a conflicting UTF-8 byte order mark, NUL bytes, or a bare carriage return cause the file to be skipped. In text output, june prints `file:1:1: ENCODING file not scanned: <reason>` as a warning; the summary counts each skipped file as a warning and includes `N files skipped, not readable as text` (`1 file` for a single skip). Other files are still judged, and a skipped file never changes the exit code. File I/O failures still stop the run with exit 2.

## Rules

june ships no rules: write your own [ast-grep](https://ast-grep.github.io) rules and keep them in `.june/rules`, or pass `--rules DIR`. A run with neither stops with an error that says to supply rules. Only an error fails the check.

Every rule has `valid` and `invalid` examples in `rule-tests/`. A normal run verifies the rules it loads first; run `june verify --rules DIR` in the CI of a repo that keeps its own rules.

### Opt-outs

Opt out of one rule on a line with a reason:

```python
def tag(items, cache={}):  # june: ignore[MY001] cache is shared on purpose
```

The marker can follow another comment, as in `# noqa: E501  # june: ignore[MY001] reason`. A later `june:` without its own `#`, such as `# type: ignore june: ignore[MY001] reason`, warns and suppresses nothing, even on a line with no finding. Name exactly one rule and give a nonblank reason after the `]`. An opt-out covers exactly one finding: if a line has several findings of that rule, it suppresses none of them and says so. A malformed comment suppresses nothing and says why. Every opt-out a change adds is printed and counted in the summary.

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
      - run: june --rules .june/rules
```

When `GITHUB_STEP_SUMMARY` is set, june appends a Markdown table of findings, opt-outs and refusals to the job summary (at most 100 rows).

For code scanning, write SARIF and upload it; `--format sarif` prints only the log, and the job still fails on an error:

```yaml
      - run: june --format sarif > june.sarif
      - if: always()
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: june.sarif
```

`--format json` prints one document of every diagnostic, for any other tool. `--format gitlab` writes the Code Quality report outside GitLab CI too (Jenkins, Buildkite, CircleCI, Azure DevOps, Bitbucket).

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
        args: [--rules, path/to/rules]  # omit to use .june/rules
```

Install june with Homebrew. The hook runs without filenames when Python files are involved and judges the staged change against HEAD. Under `--from-ref A --to-ref B`, or a pre-push that sets them, it judges the lines that range adds. A first push of a history with a root commit sets no refs, so pre-push judges nothing; CI covers it.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | No error-severity finding on a touched line. |
| `1` | An error-severity finding on a touched line with no valid opt-out. |
| `2` | No verdict: bad usage, bad rules directory, shallow clone, no base, missing, too-old, or unreadable `ast-grep`. Output: `june: error: <cause>. Fix: <instruction>`. |

Warnings, `--legacy` lines and opt-outs never change the exit code.

## More

- [VISION.md](VISION.md): purpose, scope, and the planned advisory LLM review (not built).
- [ARCHITECTURE.md](ARCHITECTURE.md): module map and invariants.
- [SECURITY.md](SECURITY.md): how to report a vulnerability.

## License

[MIT](LICENSE).
