# june

[![ci](https://github.com/vicolby/june/actions/workflows/ci.yml/badge.svg)](https://github.com/vicolby/june/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/vicolby/june)](https://github.com/vicolby/june/releases)

**Deterministic enforcement of code-guideline rules on the lines a change touches.**

june turns the guideline rules a tool can decide into CI failures that an AI agent fixes the way it fixes a ruff or mypy error.

- **Only touched lines fail.** Old violations never fail a build, so a repo adopts june with no cleanup and no baseline file.
- **Findings read like ruff.** File, line, rule id and the corrective instruction.
- **Same verdict everywhere.** No network and no model on the check path: locally, in GitHub Actions and in GitLab CI the same input gives the same result.
- **Rules are verified.** A rule whose passing and failing examples do not behave is refused.

Version 1 checks Python only.

## Quick start

```sh
brew install vicolby/tap/june
cd your-repo
june --rules "$(brew --prefix)/share/june/rules/python" --base main
```

june checks the Python files changed since the merge base of `main` and `HEAD`, and exits 1 if any error lands on a touched line.
To adopt the bundled rules, copy them to `.june/rules`, the default rules directory, and drop `--rules`:

```sh
mkdir -p .june && cp -R "$(brew --prefix)/share/june/rules/python" .june/rules
june --base main
```

## Install

june needs `ast-grep` on `PATH`. june never finds the bundled rules on its own: pass them with `--rules` or copy them to `.june/rules`.

### Homebrew

```sh
brew install vicolby/tap/june
```

Installs `june`, `ast-grep`, and the rules to `$(brew --prefix)/share/june/rules`.

### Install script

Installs the release binary and rules. In CI, pin the script and the binary to the same release. Replace `X.Y.Z` with a [release](https://github.com/vicolby/june/releases) that lists `install.sh` among its assets:

```sh
curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z sh
```

The script is plain POSIX [`install.sh`](install.sh). It detects the platform, downloads the archive and `checksums.txt`, verifies the sha256, and only then installs. It runs as one function on its last line, so a truncated download installs nothing.
Releases before the first one that attaches `install.sh` have no such asset; `https://raw.githubusercontent.com/vicolby/june/main/install.sh` serves the latest script (replace `main` with a tag to pin it).

| Variable | Meaning |
| --- | --- |
| `JUNE_VERSION` | Release to install, with or without the leading `v`. Default: the latest release. Always set it in CI. |
| `JUNE_INSTALL_DIR` | Where the binary goes. Default: `/usr/local/bin` as root, `$HOME/.local/bin` otherwise. |
| `JUNE_RULES_DIR` | Where the rules go; an existing directory is replaced. Default: `/usr/local/share/june/rules` as root, `$HOME/.local/share/june/rules` otherwise. |
| `JUNE_OS`, `JUNE_ARCH` | Override the platform: `linux` or `darwin`, `amd64` or `arm64`. |
| `JUNE_INSTALL_AST_GREP` | `1` also runs `npm install --global @ast-grep/cli`. Otherwise `ast-grep` is left alone and the script notes when it is missing. |
| `JUNE_RELEASES_URL` | Release base URL. Default: `https://github.com/vicolby/june/releases`. |

The script says so when the install directory is not on `PATH`.

### Release binary

Each [release](https://github.com/vicolby/june/releases) attaches a `tar.gz` per platform (`darwin` and `linux`, `amd64` and `arm64`), `checksums.txt` and `install.sh`.
An archive has no top-level directory: it holds the `june` binary and `rules/`. It does not include `ast-grep`.

```sh
VERSION=0.2.0 OS=linux ARCH=amd64   # VERSION without the leading v
ARCHIVE="june_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/vicolby/june/releases/download/v${VERSION}"
curl -fsSLO "${BASE}/${ARCHIVE}"
curl -fsSLO "${BASE}/checksums.txt"
grep " ${ARCHIVE}$" checksums.txt | sha256sum -c -   # on macOS: shasum -a 256 -c -
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/june"
tar -xzf "${ARCHIVE}" -C "$HOME/.local/share/june" rules
tar -xzf "${ARCHIVE}" -C "$HOME/.local/bin" june
```

## Usage

```text
june [--legacy] [--rules DIR]... [--base REF | --changed-lines FILE]
june verify [--rules DIR]...
june --version
```

| Flag | Meaning |
| --- | --- |
| `--rules DIR` | Rules directory. Repeatable. Default: `./.june/rules`. |
| `--base REF` | Judge the lines changed since the merge base of `REF` and `HEAD`. |
| `--changed-lines FILE` | Judge the lines listed in `FILE`, one `path:7` or `path:7-12` per line. `-` reads stdin. |
| `--legacy` | Also list violations on untouched lines, as information only. |
| `--version` | Print the version. |

With neither `--base` nor `--changed-lines`, june infers the base, first hit wins: pre-commit refs, the pull request or merge request base in CI, the pre-commit staged diff, the upstream branch, the remote default branch.
It never guesses: when the touched lines cannot be determined, for example in a shallow clone, it exits 2 and names the fix.
june fixes nothing itself.

## Output

```text
$ june --rules "$(brew --prefix)/share/june/rules/python" --base main
app/orders.py:11:28: PY012 opt-out: cache is shared on purpose
app/orders.py:1:23: PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.
app/orders.py:4:5: PY013 Bare except catches every exception, including KeyboardInterrupt and SystemExit. Fix: name the exception type to catch, or use except Exception.
june: 2 errors, 0 warnings, 1 opt-out; checked 1 file, 12 changed lines; base main (--base)
```

The summary ends with the base and its source: `--base`, `pre-commit refs`, `staged diff`, `pull request base`, `merge request diff base`, `upstream branch` or `remote default branch`.
That label is the only output that differs between environments. CI runs add native annotations beside the text.

## Rules

The bundled Python rules are [ast-grep](https://ast-grep.github.io) rules.

| Rule | Catches | Severity |
| --- | --- | --- |
| `PY012` | Mutable default argument | error |
| `PY013` | Bare `except` | error |
| `PY014` | Comparison to `None` with `==` or `!=` | error |
| `PY015` | `except` block that only passes | error |
| `PY016` | `assert` on a tuple, which is always true | error |

Only an error fails the check.

Each rule has a sidecar in `rule-tests/` listing code it must accept (`valid`) and flag (`invalid`). `june verify` runs every example and fails if any behaves differently:

```sh
$ june verify --rules "$(brew --prefix)/share/june/rules/python"
june: verified 5 rules, 135 examples
```

A normal run verifies the rules it loads first, so an unverified rule never produces a verdict. Run `june verify` in the CI of a repo that keeps its own rules.

## Opt-outs

Opt out of one rule on a line with a same-line comment that gives a reason:

```python
def tag(items, cache={}):  # june: ignore[PY012] cache is shared on purpose
    return cache
```

- Name exactly one rule and give a nonblank reason after the `]`.
- A malformed attempt suppresses nothing and says why.
- june prints every opt-out a change adds, honoured or not, and counts them in the summary.

## CI

### GitHub Actions

On a pull request june judges the lines changed since the pull request base and annotates each finding on the diff. It needs the base branch, so fetch full history.
Ubuntu images install Homebrew at `/home/linuxbrew/.linuxbrew` but leave it off `PATH`:

```yaml
name: june
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

On a `push` event there is no pull request base, so june exits 2. Pass `--base REF` (for example `--base origin/main`) with `fetch-depth: 0`, or run on `pull_request`.

### GitLab CI

In a merge request pipeline june judges the lines the merge request changed and writes a GitLab Code Quality report, `gl-code-quality-report.json`, beside the text. The job needs full clone depth; a shallow clone exits 2.
`before_script` installs june with the [install script](#install-script), pinned to one release (`X.Y.Z`), and `ast-grep` via `JUNE_INSTALL_AST_GREP=1`. Drop that variable if your image has `ast-grep`.

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

The report lists findings and opt-outs on touched lines: error is `major`, warning `minor`, opt-out `info`.
A detached pipeline uses the merge request diff base; a merged results or merge train pipeline uses the first parent of the merge commit.

#### Run on the default branch too

The merge request Code Quality widget compares against the latest report from the target branch. With no report there, it shows nothing useful, so also run june on the default branch.
A branch pipeline has no merge request base, so june exits 2 unless you pass `--base`; `HEAD~1` judges the latest commit:

```yaml
june:
  image: node:22  # any image with git, curl and npm
  variables:
    GIT_DEPTH: 0
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  before_script:
    - curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z JUNE_INSTALL_AST_GREP=1 sh
  script:
    - |
      if [ "$CI_PIPELINE_SOURCE" = "merge_request_event" ]; then
        june --rules .june/rules
      else
        june --rules .june/rules --base HEAD~1
      fi
  artifacts:
    when: always
    reports:
      codequality: gl-code-quality-report.json
```

### Pre-commit

Install june with Homebrew, then add the hook to `.pre-commit-config.yaml`:

```yaml
repos:
  - repo: https://github.com/vicolby/june
    rev: v0.1.0  # use the latest release tag
    hooks:
      - id: june
```

The hook runs without filenames, only when Python files are involved, and judges the staged change against HEAD. It reads `.june/rules`; to use the bundled rules, copy them there or pass the path:

```yaml
      - id: june
        args: [--rules, PREFIX/share/june/rules/python]
```

`PREFIX` is the output of `brew --prefix` (`/opt/homebrew` on Apple Silicon, `/usr/local` on Intel, `/home/linuxbrew/.linuxbrew` on Linuxbrew). pre-commit does not expand `$(brew --prefix)`.

Under `pre-commit run --from-ref A --to-ref B`, and on a pre-push that sets those variables, it judges the lines the commits in that range add.
A first push of a history with a root commit gives pre-commit no ref variables, so pre-push judges nothing; CI judges that range.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | No error-severity finding on a touched line. |
| `1` | At least one error-severity finding on a touched line with no valid opt-out. |
| `2` | No verdict: bad usage, a missing or faulty rules directory, a shallow clone, no base, a missing `ast-grep`. Output is `june: error: <cause>. Fix: <instruction>`. |

Warnings, `--legacy` lines and opt-outs never change the exit code.

## More

- [VISION.md](VISION.md): what june is for and what it refuses to become, including the planned advisory LLM review (not built).
- [ARCHITECTURE.md](ARCHITECTURE.md): modules, guarantees and design.
