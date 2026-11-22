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

### Release binary

Each [release](https://github.com/vicolby/june/releases) attaches a `tar.gz` per platform (`darwin` and `linux`, `amd64` and `arm64`) and a `checksums.txt`.
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
june never finds the bundled rules on its own, so pass them with `--rules "$HOME/.local/share/june/rules/python"`, or copy them to `.june/rules`.

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

```yaml
june:
  image: <an image with june and ast-grep installed>
  variables:
    GIT_DEPTH: 0
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  script:
    - june --rules .june/rules
  artifacts:
    when: always
    reports:
      codequality: gl-code-quality-report.json
```

The report lists findings and opt-outs on touched lines; an error is `major`, a warning `minor` and an opt-out `info`.
A detached pipeline uses the merge request diff base; a merged results or merge train pipeline uses the first parent of the merge commit.

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
