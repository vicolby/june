# june

[![ci](https://github.com/vicolby/june/actions/workflows/ci.yml/badge.svg)](https://github.com/vicolby/june/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/vicolby/june)](https://github.com/vicolby/june/releases)

**Deterministic enforcement of code-guideline rules on the lines a change touches.**

june turns the guideline rules a tool can decide into CI failures that an AI agent fixes the way it fixes a ruff or mypy error.
It is built for teams whose agents write code against shared guideline skills: the skill keeps only judgment, taste and design guidance, and june checks the rest.

- **Only touched lines fail.** june judges the lines a change adds or edits, never the enclosing function or file. Old violations never fail a build, so a repo adopts june with no cleanup and no baseline file.
- **Findings read like ruff.** Each one names the file, line, rule id and the corrective instruction.
- **Same verdict everywhere.** No network and no model on the check path: the same input gives the same result locally, in GitHub Actions and in GitLab CI.
- **Rules are verified.** Every rule ships with passing and failing examples, and june refuses a rule whose examples do not behave.

Version 1 checks Python only.

## Quick start

```sh
brew install vicolby/tap/june
cd your-repo
june --rules "$(brew --prefix)/share/june/rules/python" --base main
```

june finds the lines changed since the merge base of `main` and `HEAD`, checks the changed Python files, and exits 1 if any error lands on a touched line.
To adopt the bundled rules for good, copy them to `.june/rules`, the default rules directory, and drop `--rules`:

```sh
mkdir -p .june && cp -R "$(brew --prefix)/share/june/rules/python" .june/rules
june --base main
```

## Install

### Homebrew

```sh
brew install vicolby/tap/june
```

Installs the `june` binary, `ast-grep` as a dependency, and the bundled rules to `$(brew --prefix)/share/june/rules`.

### Install script

One line installs the release binary and the bundled rules. It suits CI, where it replaces a download, checksum and extract block.
Pin both the script and the binary to a release: fetch `install.sh` from that release's assets and set `JUNE_VERSION` to the same version, so a CI run does not change when `main` or a new release does.
Replace `X.Y.Z` with a [release](https://github.com/vicolby/june/releases) that lists `install.sh` among its assets:

```sh
curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z sh
```

To read the script before it runs, download it, read it, then run it:

```sh
curl -fsSLo install.sh https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh
less install.sh
JUNE_VERSION=X.Y.Z sh install.sh
```

Install steps live in one function called on the last line, so a truncated download defines functions at most and cannot leave a partial install.
Releases before the one that first attaches `install.sh` have no such asset. Until you pin one, `https://raw.githubusercontent.com/vicolby/june/main/install.sh` serves the latest script.

`JUNE_VERSION` defaults to the latest release for interactive use; CI should always set it.
The script detects `linux` or `darwin` and `amd64` or `arm64`, downloads the release archive and `checksums.txt`, verifies the sha256 and only then installs. It stops with a clear error on an unsupported platform, a missing release or a checksum mismatch, and prints the installed version when done.
Read it before you pipe it to `sh`: it is [`install.sh`](install.sh), plain POSIX `sh`. To pin that raw URL, replace `main` with a release tag.

Settings are environment variables:

| Variable | Meaning |
| --- | --- |
| `JUNE_VERSION` | Release to install, with or without the leading `v`. Default: the latest release. |
| `JUNE_INSTALL_DIR` | Where the `june` binary goes. Default: `/usr/local/bin` as root (the usual CI user), `$HOME/.local/bin` otherwise. |
| `JUNE_RULES_DIR` | Where the bundled rules go; an existing directory is replaced. Default: `/usr/local/share/june/rules` as root, `$HOME/.local/share/june/rules` otherwise. |
| `JUNE_OS`, `JUNE_ARCH` | Override the detected platform: `linux` or `darwin`, `amd64` or `arm64`. |
| `JUNE_INSTALL_AST_GREP` | Set to `1` to also run `npm install --global @ast-grep/cli`. |
| `JUNE_RELEASES_URL` | Release base URL. Default: `https://github.com/vicolby/june/releases`. |

```sh
curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z JUNE_INSTALL_DIR=/usr/local/bin sh
```

The archive does not contain `ast-grep`, and the script installs it only when you ask with `JUNE_INSTALL_AST_GREP=1`. That keeps the default free of a Node.js requirement and of a global npm install that many images already cover. Without the flag the script leaves an existing `ast-grep` untouched and prints a note when `ast-grep` is not on `PATH`.
june never finds the bundled rules on its own, so pass them with `--rules`, for example `--rules "$HOME/.local/share/june/rules/python"`, or copy them to `.june/rules`.
If the install directory is not on `PATH`, the script says so.

### Release binary

To install by hand, each [release](https://github.com/vicolby/june/releases) attaches a `tar.gz` per platform (`darwin` and `linux`, `amd64` and `arm64`), `checksums.txt`, and `install.sh`.
An archive holds the `june` binary and a `rules/` directory. It does not install `ast-grep`: install that separately and put it on `PATH`, for example `npm install --global @ast-grep/cli`.

Set `VERSION` to a release without the leading `v`, and `OS` and `ARCH` to the platform:

```sh
VERSION=0.2.0 OS=linux ARCH=amd64
ARCHIVE="june_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/vicolby/june/releases/download/v${VERSION}"
curl -fsSLO "${BASE}/${ARCHIVE}"
curl -fsSLO "${BASE}/checksums.txt"
grep " ${ARCHIVE}$" checksums.txt | sha256sum -c -   # on macOS: shasum -a 256 -c -
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/june"
tar -xzf "${ARCHIVE}" -C "$HOME/.local/share/june" rules
tar -xzf "${ARCHIVE}" -C "$HOME/.local/bin" june
june --version
```

The archive has no top-level directory, so extract it where you want: the binary on `PATH`, `rules/` anywhere readable.

## Usage

```text
june [--legacy] [--rules DIR]... [--base REF | --changed-lines FILE]
june verify [--rules DIR]...
june --version
```

| Flag | Meaning |
| --- | --- |
| `--rules DIR` | Rules directory. Repeatable. Defaults to `./.june/rules`. Bundled rules are opt-in: june never loads them on its own. |
| `--base REF` | Judge the lines changed since the merge base of `REF` and `HEAD`. |
| `--changed-lines FILE` | Judge the lines listed in `FILE`, one `path:7` or `path:7-12` per line. `-` reads stdin. |
| `--legacy` | Also list violations on untouched lines, as information only. They never change the exit code. |
| `--version` | Print the version. |

With no `--base` and no `--changed-lines`, june infers the base: the pull request or merge request base in CI, otherwise the upstream branch or the remote default branch.
It never guesses: when the touched lines cannot be determined, for example in a shallow clone, june exits 2 and names the fix.
june fixes nothing itself, so the agent always sees what was wrong.

## How findings look

A feature branch that adds a mutable default, a bare `except`, a `pass`-only handler and an `== None` comparison, plus one deliberate opt-out:

```text
$ june --rules "$(brew --prefix)/share/june/rules/python" --base main
app/orders.py:11:28: PY012 opt-out: cache is shared on purpose
app/orders.py:1:23: PY012 Mutable default argument. Fix: default the parameter to None and create the value inside the function.
app/orders.py:4:5: PY013 Bare except catches every exception, including KeyboardInterrupt and SystemExit. Fix: name the exception type to catch, or use except Exception.
app/orders.py:5:9: PY015 Except block only passes, which silently swallows the exception. Fix: handle the exception, log it, or re-raise it.
app/orders.py:6:17: PY014 Comparison to None with == or !=. Fix: use is None or is not None.
june: 4 errors, 0 warnings, 1 opt-out; checked 1 file, 12 changed lines; base main (--base)
```

The summary line ends with the base and where it came from (`--base`, `pull request base`, `merge request diff base`, `upstream branch` or `remote default branch`).
The same lines are the whole output everywhere; CI runs add native annotations beside them.

## Rules

The bundled Python rules are [ast-grep](https://ast-grep.github.io) rules that june owns directly.

| Rule | Catches | Severity |
| --- | --- | --- |
| `PY012` | Mutable default argument | error |
| `PY013` | Bare `except` | error |
| `PY014` | Comparison to `None` with `==` or `!=` | error |
| `PY015` | `except` block that only passes | error |
| `PY016` | `assert` on a tuple, which is always true | error |

Only an error fails the check, so a rule that is right most of the time can ship as a warning.

### How rules are verified

Each rule has a sidecar in `rule-tests/` listing code it must accept (`valid`) and code it must flag (`invalid`).
`june verify` runs every example of every rule and refuses the run if any behaves differently:

```sh
$ june verify --rules "$(brew --prefix)/share/june/rules/python"
june: verified 5 rules, 135 examples
```

A normal run verifies the rules it loads first, so an unverified rule can never produce a verdict.
Run `june verify` in the CI of a repo that keeps its own rules.

## Opt-outs

A line can opt out of one named rule with a same-line comment that carries a written reason:

```python
def tag(items, cache={}):  # june: ignore[PY012] cache is shared on purpose
    return cache
```

- Name exactly one rule, and give a nonblank reason after the `]`.
- A malformed attempt suppresses nothing and says why.
- june prints every opt-out a change adds, honoured or not, so a reviewer sees them in the log and in the summary count.

## GitHub Actions

On a pull request june judges the lines changed since the pull request base and annotates each finding on the diff, beside the same plain text.
It needs the base branch in the checkout, so fetch the full history.
Ubuntu images install Homebrew at `/home/linuxbrew/.linuxbrew` and leave it off `PATH`, so add its `bin` directory before `brew install`:

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

On a `push` event there is no pull request base, so june exits 2 and names the fix: pass `--base REF` (for example `--base origin/main`) with `fetch-depth: 0`, or run on `pull_request`.

## GitLab CI

In a merge request pipeline june judges the lines the merge request changed and writes a GitLab Code Quality report, `gl-code-quality-report.json`, beside the plain text.
The job needs full clone depth so the base is in the clone; with a shallow clone june exits 2 and names the fix.
The `before_script` line installs june with the [install script](#install-script) and, with `JUNE_INSTALL_AST_GREP=1`, `ast-grep`. Skip that variable if your image already has `ast-grep`; the script and `JUNE_VERSION` are pinned to the same release, `X.Y.Z` here.

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

The report lists findings and opt-outs on touched lines; an error is `major`, a warning `minor` and an opt-out `info`.
A detached pipeline uses the merge request diff base; a merged results or merge train pipeline uses the first parent of the merge commit.

### Run on the default branch too

The merge request Code Quality widget compares the merge request's report with the report from the latest pipeline on the target branch. If no pipeline on that branch has produced `gl-code-quality-report.json`, for example when the job only runs in merge request pipelines, the widget has nothing to compare against and shows no useful result.
So also run june on the default branch with the same `artifacts.reports.codequality`.
A branch pipeline has no merge request base, so june exits 2 unless you pass `--base`; `HEAD~1` judges the lines the latest commit changed:

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

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | No error-severity finding on a touched line. |
| `1` | At least one error-severity finding on a touched line with no valid opt-out. |
| `2` | june could not reach a verdict: bad usage, a missing or faulty rules directory, a shallow clone, no base, a missing `ast-grep`. Output is `june: error: <cause>. Fix: <instruction>`. |

Warnings, `--legacy` lines and opt-outs never change the exit code.

## Roadmap

Version 2 plans an advisory LLM review of the taste and design guidelines no tool can decide, as a separate command that never fails a build.
A pre-commit hook is also planned. Neither exists yet; see [VISION.md](VISION.md).

## Learn more

- [VISION.md](VISION.md): what june is for, what it owns and what it refuses to become.
- [ARCHITECTURE.md](ARCHITECTURE.md): the modules, the guarantees and the design behind them.
