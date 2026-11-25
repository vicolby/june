# june

[![ci](https://github.com/vicolby/june/actions/workflows/ci.yml/badge.svg)](https://github.com/vicolby/june/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/vicolby/june)](https://github.com/vicolby/june/releases)

**Deterministic enforcement of code-guideline rules on the lines a change touches.**

june turns the guideline rules a tool can decide into CI failures that an AI agent fixes like a ruff or mypy error.

- **Only touched lines fail.** Old violations never fail a build: no cleanup, no baseline file.
- **Same verdict everywhere.** No network, no model: the same input gives the same result locally, in GitHub Actions and in GitLab CI. See [Promises](#promises).
- **Rules are verified.** A rule whose passing and failing examples misbehave is refused.

Version 1 checks Python only: files ending in `.py`. `.pyi` stubs and scripts with no extension (even with a Python shebang) are not read.

## Promises

- **No telemetry.** june collects and sends no usage data, crash reports or identifiers.
- **No network on the check path.** `june`, `june verify` and `june new` open no network connection, and run only `git` and `ast-grep` as local subprocesses. Checked by running a check and `june verify` with network access denied: both gave their normal verdict. Only the experimental [`june review`](docs/ai-review.md) opens a connection, to the OpenAI API, and a test fails the build if any package outside `internal/openai` imports `net/http`, and none of the packages that decide a check depends on `net`.
- Network is used only to install, and by `june review` when you opt in: `install.sh` downloads the release, `JUNE_INSTALL_AST_GREP=1` runs `npm`, and the GitHub Action does both. The promise covers june's own code; it was not audited for `git` or `ast-grep`, though the sandboxed run above included both.

## Why june

june is not a linter with a rule set. It enforces your own team's guidelines as ast-grep rules, on the lines a change touches. Compared with the usual alternatives, as of the versions current when this was written:

| | june | ruff + reviewdog | Semgrep with a baseline |
| --- | --- | --- | --- |
| Rules | Yours, in YAML, each verified against examples. | Ruff's built-in rules; ruff has no user-defined rules. | Yours or the registry's, in YAML. |
| Old violations | Only touched lines can fail; [`--legacy`](#usage) can list untouched findings as information. | reviewdog filters findings to the diff when configured to; ruff itself reports the whole file. | `--baseline-commit` reports findings new since that commit, compared by finding, not by line. |
| Rule quality | A rule whose examples misbehave is refused. | Not applicable. | Rule tests are optional (`semgrep --test`). |
| Network | None on the check path. | None for ruff; reviewdog's [default local reporter](https://github.com/reviewdog/reviewdog#reporter-local--reporterlocal-default) filters locally without posting to a forge. Forge API reporters use the network. | Registry rules, metrics and version checks use the network; avoid those requests with local rules and `--metrics=off --disable-version-check` ([CLI reference](https://docs.semgrep.dev/cli-reference)). |

Pick ruff for the standard Python lint rules, and Semgrep for a large shared rule registry or taint analysis. Pick june when the rules are your own guidelines, an AI agent has to fix their failures, and the gate must be the same everywhere. It is Python only in version 1.

## Install

june needs Git to locate repository defaults, including with `--changed-lines`, `verify` and `new`. It also needs `ast-grep` 0.51.1 or newer on `PATH`, reporting a numeric `X.Y.Z` release; prerelease and other suffixes are refused. A check or `june verify` with an older or unreadable version exits 2 with the fix. 0.51.3 is the tested version, and `june --version` and a successful `june verify` print the one in use. See [Rules](#rules) to supply your own rules.

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
| `JUNE_VERIFY` | `1` enables release authentication as described below. Default: sha256 check only. |
| `JUNE_RELEASES_URL` | Default: `https://github.com/vicolby/june/releases`. |

The script is plain POSIX [`install.sh`](install.sh); read it before piping to `sh`.

**Release archive.** Each release attaches `june_<version>_<os>_<arch>.tar.gz` (`darwin`/`linux`, `amd64`/`arm64`), `checksums.txt` and `install.sh`. The archive has no top-level directory: it holds `june`, not `ast-grep`.

**Verify a release.** The sha256 check in `install.sh` catches corruption only, because `checksums.txt` comes from the same release as the archive. Releases are also signed by the release workflow: `checksums.txt` carries a keyless cosign signature (`checksums.txt.sigstore.json`), each archive has a build provenance attestation and an SPDX SBOM (`<archive>.sbom.json`). Verify with either tool (replace `X.Y.Z`, platform and arch):

```sh
version=X.Y.Z
archive="june_${version}_linux_amd64.tar.gz"
identity="https://github.com/vicolby/june/.github/workflows/release.yml@refs/tags/v${version}"
subjects=$(gh attestation verify "$archive" --repo vicolby/june \
  --cert-identity "$identity" --source-ref "refs/tags/v${version}" \
  --format json --jq '.[].verificationResult.statement.subject[] | [.name, .digest.sha256] | @tsv') &&
  printf '%s\n' "$subjects" | awk -v f="$archive" -v d="$(sha256sum "$archive" | cut -d ' ' -f 1)" \
    '$1 == f && $2 == d { found = 1 } END { exit !found }'

cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
```

The gh command requires a signed subject with the requested archive name and digest. For cosign, also compare the archive's sha256 against its exact filename in the verified `checksums.txt`. On macOS, use `shasum -a 256` in place of `sha256sum`.

`JUNE_VERIFY=1 sh install.sh` authenticates the requested version's release workflow and archive using `gh` when installed, otherwise `cosign`. Missing tools, failed verification or a checksum mismatch stop installation; a failed `gh` verification does not fall back to `cosign`. Releases before the first signed one have no signature, so verification fails for them.

## Usage

```text
june [--format FORMAT] [--legacy] [--ignore-whitespace] [--rules DIR]... [--exclude GLOB]... [--base REF | --changed-lines FILE] [PATH]...
june init [--ci github|gitlab]
june verify [--rules DIR]... [--rule ID] [--format FORMAT]
june new [--rules DIR] RULE
june review --rules FILE --model MODEL [--effort LEVEL] [--skill PATH | --prompt TEXT] [--base REF | --changed-lines FILE] [--exclude GLOB]... [--format FORMAT] [PATH]...
june --version
june --help | -h
```

| Flag | Meaning |
| --- | --- |
| `PATH` | Narrow the check to these files and directories, relative to the working directory. Paths must match the repository spelling; case and symlinks are not normalized. Paths left out by narrowing are not counted as excluded. An absolute path, a path that does not exist, or a path that lies outside the working directory exits 2. Flags must precede paths; prefix paths starting with `-` with `./`. |
| `--rules DIR` | Rules directory, relative to the working directory; repeatable for checks and `verify`; `new` takes one. Default: `rules` in `.june/config.yml`, else `.june/rules`. |
| `--ci PROVIDER` | With `june init`: write `github` (default) or `gitlab` CI configuration. |
| `--base REF` | Judge lines changed since the merge base of `REF` and `HEAD`. |
| `--changed-lines FILE` | Judge lines listed in `FILE`, one `path:7` or `path:7-12` per line; `-` reads stdin. |
| `--format FORMAT` | `text`, `json`, `sarif`, `github` or `gitlab`. Default: `github` under GitHub Actions, `gitlab` under GitLab CI, else `text`. `june verify` takes it too. |
| `--rule ID` | With `june verify`: check the examples of that one rule only. |
| `--exclude GLOB` | Keep paths out of the check, repeatable. Adds to `exclude` in `.june/config.yml`. |
| `--legacy` | Also list violations on untouched lines, as information only. |
| `--ignore-whitespace` | Do not count a line as touched when only its whitespace changed, like `git diff -w`. Strict by default. Not valid with `--changed-lines`. |
| `--model MODEL`, `--effort LEVEL`, `--skill PATH`, `--prompt TEXT` | With `june review`: the model (required), its reasoning effort, and the replacement for the default instruction (one of the two, not both). `--rules` is a single Markdown file there. See [AI review](docs/ai-review.md). |
| `--help`, `-h` | Print usage and flags to stdout and exit 0. |

Use a full-history git checkout.

```sh
june --rules path/to/rules --base main
june src/api tests/test_api.py   # only those paths
```

`.june/rules` and `.june/config.yml` are found at the repository root, so a run from `svc/` uses the same rules as one from the root. Outside git they resolve from the working directory. Finding paths stay relative to the working directory, except GitHub annotations and SARIF, which use repository-root paths when the root is known.

### Setting up

`june init` requires a git repository and sets it up at the root: `.june/rules` with a starter rule from `june new` that passes `june verify`, a commented `.june/config.yml`, one CI snippet and `.pre-commit-config.yaml` for [pre-commit](#pre-commit). By default it writes `.github/workflows/june.yml` using the [GitHub Action](#github-actions). Use `june init --ci gitlab` to write `.june/gitlab-ci.yml` instead, then add `include: [{local: .june/gitlab-ci.yml}]` to `.gitlab-ci.yml`. The snippets pin the june release of the binary that wrote them; a build without a release version writes `X.Y.Z`, to replace. Running it again preserves existing files and rules.

Without `--base` or `--changed-lines`, june infers the base, first hit wins: pre-commit refs, CI event base (see [GitHub Actions](#github-actions) and [GitLab CI](#gitlab-ci)), pre-commit staged diff, config base, upstream branch, remote default branch. The summary names the one used. When the touched lines cannot be determined (a shallow clone, say) june exits 2 and names the fix.

### Source encoding

june defaults to UTF-8 and honors a PEP 263 coding declaration (e.g. `# -*- coding: latin-1 -*-`) on the first line, or the second when the first is blank or comment-only. Supported encodings are `utf-8`, `latin-1`, `ascii` and `cp1252`; transcoding preserves line numbers.

Unsupported declarations, invalid bytes for the selected encoding, a conflicting UTF-8 byte order mark, NUL bytes, or a bare carriage return cause the file to be skipped. In text output, june prints `file:1:1: ENCODING file not scanned: <reason>` as a warning; the summary counts each skipped file as a warning and includes `N files skipped, not readable as text` (`1 file` for a single skip). Other files are still judged, and a skipped file never changes the exit code. File I/O failures still stop the run with exit 2.

### Coverage notes

Notes and warnings never change the exit code. Coverage notes appear in the summary in every format; SARIF carries the summary as a `note` in `runs[].invocations[].toolExecutionNotifications`.

- **Nothing checked.** When no changed `.py` file remains in scope, the summary includes `note: no changed Python files found`. If you expected some, check `--base` and the checkout (a `pull_request_target` checkout, say, has no diff).
- **Paths left out.** In git-derived scope, changed `.py` paths that are symlinks or submodules are not read; the summary counts them as `N changed .py paths skipped, not regular files (symlink or submodule)`. A local run also counts changed paths absent from a sparse checkout as `N changed .py paths skipped, absent from sparse checkout`. Counts use `path` for a single skip. For unreadable text, see [Source encoding](#source-encoding).
- **Syntax errors.** A touched line inside a region the parser could not read gets `file:line:col: PARSE ...` as a warning, at most once per file, because rules matched only what the parser recovered there. Fix the syntax error.

## Rules

june ships no rules: write your own [ast-grep](https://ast-grep.github.io) rules and select their directories through [`--rules` or repository defaults](#usage). A run with no rules stops with an error that says to supply rules. Only an error fails the check.

Every rule has `valid` and `invalid` examples in `rule-tests/`. A normal run verifies the rules it loads first; run `june verify --rules DIR` in the CI of a repo that keeps its own rules. To write one, start from `june new RULE` and see [Writing rules](docs/writing-rules.md).

A rule's optional `note` and `url` print under each of its findings and travel into the JSON, SARIF, GitHub and GitLab output. Rules can share utility rules kept in a `utils/` directory beside the rule files. `june verify --rule ID` checks one rule's examples, and `--format json` makes a failing example machine-readable, with the examples file, the rule and the example's number. The rule ID `june-parse-error` is reserved for the built-in syntax-error scan; user rules with that ID, ignoring case, are refused.

A rule can scope itself with ast-grep's `files:` and `ignores:`, matched against the path relative to the working directory. If a file cannot be staged at its original path, a run using path-scoped rules exits 2. `june verify` refuses unsupported rule keys (such as `fix` or `labels`), naming the key, and reports bad globs against the offending rule file.

### From guideline to rule

1. `june new MY001` writes a rule and its examples that already verify.
2. Edit the pattern, the `message` (say what to do) and the `valid` and `invalid` examples together.
3. `june verify --rule MY001` until it exits 0, then `june verify` for all rules.
4. `june --rules .june/rules --base main` to see the rule on a real change.

**Drafting with an LLM.** Give the model one guideline, `june new` as the starting point and the loop below to check the draft against its examples. Prompt:

```text
Write a june rule MY001 for this guideline: "<guideline>". Run `june new MY001`, then edit
.june/rules/MY001.yml and .june/rules/rule-tests/MY001-test.yml. Write the examples first,
including the edge cases the guideline mentions. Run `june verify --rule MY001 --format json`,
read each diagnostic, fix the rule or the examples, and repeat until it exits 0.
Then run `june verify`. The message must say what to do. Do not use the keys fix or labels.
```

A human still reviews the rule and its examples: a verified rule matches its examples, not necessarily the guideline. The loop with real output is in [Writing rules](docs/writing-rules.md#worked-example-a-model-drafts-a-rule).

### Config file

`.june/config.yml` holds a run's defaults. Every key is optional; unknown or repeated keys and extra YAML documents are errors:

```yaml
rules:            # rules directories, default .june/rules; --rules replaces them
  - .june/rules
base: origin/main # used when no --base, CI event, merge request or pre-commit scope names a base
exclude:          # see below
  - vendor
```

`rules` directories are relative to the directory that holds `.june`. An empty entry means `.june/rules`; directories that name the same location count once. `june new` requires one effective directory; with multiple configured directories, choose one with `--rules DIR`. A flag wins over the config: `--rules` replaces `rules`, `--base` replaces `base`, and `--changed-lines` makes `base` moot; `--exclude` adds to `exclude` (see [Excluding paths](#excluding-paths)).

### Excluding paths

Exclude generated code, vendored trees and fixtures by path, in `.june/config.yml` or with `--exclude`:

```yaml
exclude:
  - vendor
  - "**/*_pb2.py"
  - migrations/**
```

Globs in the config are relative to the directory that holds `.june`, and `--exclude` globs to the working directory: `*` stays within one path segment, `**` spans directories, a glob that matches a directory excludes everything under it, and whitespace in a glob is literal. Invalid globs are errors.

Exclusion applies during discovery, before file-existence checks or reading excluded worktree contents. Excluded files are not scanned or counted as checked, and the summary reports how many were excluded. Unreadable excluded worktree files do not refuse the run. Excluded Python paths in `--changed-lines` need not exist, but list entries must still use valid `path:7` or `path:7-12` syntax.

### Opt-outs

Opt out of one rule on a line with a reason:

```python
def tag(items, cache={}):  # june: ignore[MY001] cache is shared on purpose
```

june considers only the first `june:` marker in each Python comment; text in string literals is not an opt-out. The marker can follow another comment segment, as in `# noqa: E501  # june: ignore[MY001] reason`. On a touched line, a marker without its own `#`, such as `# type: ignore june: ignore[MY001] reason`, warns and suppresses nothing, even when there is no finding. Name exactly one loaded rule and give a nonblank reason after the `]`.

An opt-out covers exactly one finding: if a line has several findings of that rule, it suppresses none of them and says so. Fix a violation or split the line. The same suppression limit applies to untouched lines listed with `--legacy`. A malformed comment suppresses nothing and says why. Every opt-out on a touched line is printed and counted in the summary; opt-outs on untouched lines stay silent.

## CI

### GitHub Actions

The action installs june with the [install script](#install) and the tested ast-grep, then runs june. Pin it to a release tag (replace `X.Y.Z`); that tag also picks the june version. For any other ref set `version: X.Y.Z`. Fetch full history:

```yaml
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
      - uses: vicolby/june@vX.Y.Z
```

Inputs: `rules` (default: repository config, else `.june/rules`), `base` (a git ref to judge against), `version`.

On a pull request june judges lines changed since the PR base and annotates the diff. In a merge queue (`merge_group`) it judges the group against `merge_group.base_sha`. When inferring that base, an unreadable or invalid event payload exits 2 instead of falling back to another branch; provide a readable `GITHUB_EVENT_PATH` with a valid `merge_group.base_sha`, or set `base` explicitly.

If a pull request event judges 0 changed Python lines, june warns; if HEAD is the base, it exits 2. These safeguards also apply with an explicit base, supplied changed lines or pre-commit scopes. `pull_request_target` checks out the base branch by default, which is that case: use `pull_request`, or check out the pull request head.

Without the action, install june with `brew install vicolby/tap/june` or the install script above, and run `june`; `--base origin/main` on a push.

When `GITHUB_STEP_SUMMARY` is set, june appends a Markdown table of findings, opt-outs and run diagnostics to the job summary (at most 100 rows).

For code scanning, write SARIF and upload it; `--format sarif` prints only the log, and the job still fails on an error:

```yaml
      - run: june --format sarif > june.sarif
      - if: always()
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: june.sarif
```

`--format json` prints one document of every diagnostic, for any other tool. `--format gitlab` writes the Code Quality report outside GitLab CI too (Jenkins, Buildkite, CircleCI, Azure DevOps, Bitbucket).

On `push` there is no PR base: supply one with `--base origin/main` or the [config file](#config-file), and keep `fetch-depth: 0`; otherwise june exits 2. Alternatively, run on `pull_request`.

### GitLab CI

In a merge request pipeline june judges the lines the MR changed and writes a Code Quality report (error `major`, warning `minor`, opt-out `info`). Use full clone depth; a shallow clone exits 2. `X.Y.Z` is a pinned release as above.

```yaml
june-check:
  image: node:22  # any image with git, curl and npm
  variables:
    GIT_DEPTH: 0
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  before_script:
    - curl -fsSL https://github.com/vicolby/june/releases/download/vX.Y.Z/install.sh | JUNE_VERSION=X.Y.Z JUNE_INSTALL_AST_GREP=1 sh
  script:
    # A merge request pipeline names its own base. A push is judged since CI_COMMIT_BEFORE_SHA.
    # That variable is forty zeros for a schedule, a manual run, or the first commit, which uses HEAD~1.
    - |
      if [ -n "$CI_MERGE_REQUEST_IID" ]; then
        june
      else
        base="$CI_COMMIT_BEFORE_SHA"
        if [ "$base" = "0000000000000000000000000000000000000000" ]; then
          base="HEAD~1"
        fi
        june --base "$base"
      fi
  artifacts:
    when: always
    reports:
      codequality: gl-code-quality-report.json
```

The job is named `june-check` so it does not clash with an existing `june` job; rename it freely. The MR widget compares against the latest report on the target branch, so the job also runs on the default branch. A branch pipeline has no MR base: a push is judged since `$CI_COMMIT_BEFORE_SHA`, and a schedule, a manual run, or the first commit (where GitLab sets that variable to forty zeros) is judged against `HEAD~1`. The first commit has no parent, so that pipeline still exits 2.

### Pre-commit

```yaml
repos:
  - repo: https://github.com/vicolby/june
    rev: v0.5.0  # latest release tag; v0.3.0 is the first with the hook
    hooks:
      - id: june
        args: [--rules, path/to/rules]  # omit to use the configured rules
```

Install june with Homebrew. The hook runs without filenames when Python files are involved and judges the staged change against HEAD. Under `--from-ref A --to-ref B`, or a pre-push that sets them, it judges the lines that range adds. A first push of a history with a root commit sets no refs, so pre-push judges nothing; CI covers it.

### AI review (experimental)

`june review` asks an OpenAI model whether the touched lines break the guidelines in one Markdown file of your rules, for the taste and design rules no tool can decide. It runs only in GitHub Actions and GitLab CI, never as part of a bare `june`, and every finding is a warning: it never fails a build.

```yaml
      - uses: vicolby/june@vX.Y.Z
        with:
          ai-model: ${{ vars.JUNE_AI_MODEL }}
          ai-rules: docs/ai-rules.md
          openai-api-key: ${{ secrets.OPENAI_API_KEY }}
```

The action inputs are `ai-model` (empty skips the review), `ai-rules`, `ai-effort`, `ai-skill`, `ai-prompt` and `openai-api-key`. The review step runs after the check and, like any later step, only if the check did not fail. A pull request from a fork has no secrets, so there the review only warns that it had no key.

What it sends, what it costs, the GitLab job, the failure warnings and why a finding has no opt-out are in [AI review](docs/ai-review.md). It sends the touched Python files and your rules file to OpenAI with your key, so read [What leaves the machine](docs/ai-review.md#what-leaves-the-machine) before enabling it. This release has not run against the live OpenAI API.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | No error-severity finding on a touched line. |
| `1` | An error-severity finding on a touched line with no valid opt-out. |
| `2` | No verdict: bad usage, bad rules directory, shallow clone, no base, missing, too-old, or unreadable `ast-grep`. Output: `june: error: <cause>. Fix: <instruction>`. |

Warnings, `--legacy` lines and opt-outs never change the exit code. `june review` uses the same codes: it exits 0 whatever the model says or fails to say, and 2 for bad usage, a bad rules file, no scope, or a run outside GitHub Actions and GitLab CI.

Unexpected internal errors include the issue URL and june version, and ask you to report the command you ran.

## More

- [VISION.md](VISION.md): purpose and scope, including the advisory LLM review.
- [docs/ai-review.md](docs/ai-review.md): `june review`, the experimental model review of taste and design rules.
- [docs/writing-rules.md](docs/writing-rules.md): writing and verifying rules, including with a model in the loop.
- [ARCHITECTURE.md](ARCHITECTURE.md): module map and invariants.
- [COMPATIBILITY.md](COMPATIBILITY.md): what is stable, what may change and what 1.0 freezes.
- [SECURITY.md](SECURITY.md): how to report a vulnerability.

## License

[MIT](LICENSE).
