# Architecture

This document applies VISION.md.
Where the two disagree, VISION.md wins.
The module interfaces and commands below describe the target design; see [cmd/june](#cmdjune) for current usage.
Vocabulary: module, interface, implementation, depth, seam, adapter, leverage, locality.

## Shape

`june` is four modules.
Callers cross each interface and nothing else, and tests cross the same interfaces.

```
cmd/june -> check -> rules   (Load, Scan)
                  -> diag    (Render)
```

Layout: `cmd/june`, `internal/{check,rules,diag}`, `rules/python`.

## check

`check` is the deep module.
It decides one change: which lines are touched, which findings land on them, which opt-outs apply, and the exit code.

```go
func Change(ctx context.Context, env Env, req Request) (Result, error)
func Rules(ctx context.Context, env Env, dirs []string) (Result, error)

type Env struct{ Dir string; Vars map[string]string } // os.Environ snapshot; nothing reads ambient state
type Request struct {
	Paths   []string  // narrows only
	Base    string    // git ref; "" = inferred
	Changed io.Reader // supplied list, one "path:7" or "path:7-12" per line; replaces git
	Rules   []string  // merged directories; none = ./.june/rules
	Format  diag.Format // "" = inferred
	Legacy  bool      // also list untouched-line violations, information only
}
type Result struct{ Exit int; Out []byte; Files []diag.File; Diags []diag.Diagnostic }
```

Invariants:
- Exit 1 if and only if an error-severity match is anchored on a touched line with no valid opt-out.
- Warnings, legacy matches and opt-out lines never change Exit.
- A touched line is one added or edited in the post-image.
- Pure deletions, mode changes and pure renames touch nothing.
- A new file touches every line, including an untracked file in a local run.
- A match is judged on its first line only, so rules match the smallest wrong node.
- The touched set is unknowable means Exit 2, never 0 and never every line.
- Out is a pure function of the request, the tree and the diff: sorted, no clock, no absolute path, no ANSI.
- Exit and finding diagnostics are identical across formats and CI vendors.
- The opt-out is a same-line Python comment `# june: ignore[PY012] <reason>`, naming exactly one rule from the loaded rules. The spaces after `#` and `june:` are literal single spaces, rule IDs have no surrounding whitespace, and a space must separate `]` from the required nonblank reason.
- A comment whose content starts with `june:` after leading whitespace is an opt-out attempt; malformed attempts suppress nothing and explain why with a Fix. The same text in a string literal is not an opt-out.
- Every opt-out on a touched line is printed, honoured or not. A valid opt-out with no matching finding says it suppresses nothing; opt-outs on untouched lines are silent.

Order: touched set, then `rules.Load`, then scan changed Python files only, then keep touched anchors, then opt-outs, then severity, then render.

Scope inference, first hit wins: `Changed`, `Base`, pre-commit refs, GitHub pull request base, GitLab merge request base, pre-commit staged diff, then local (merge base with the upstream branch or the remote default branch, worktree plus untracked).
The summary line prints the base and where it came from.

Errors:
- `Change` returns an `error` for cancellation or when june cannot produce an explanation.
- Other outcomes june can explain, such as a shallow clone, a missing merge base, a push event with no base, a rule fault, a missing ast-grep or a bad list, come back as a diagnostic with Exit 2.
- `cmd/june` explains cancellation, a working-directory lookup failure and a report file that cannot be written as Exit 2 diagnostics, preserving the lookup or write cause. The report failure follows the verdict text already written. It reserves the june-bug message for unexplained errors.
- Each reads `june: error: <cause>. Fix: <instruction>`.
- A cancelled context abandons changed-list reads and kills child processes.

It hides: repo discovery, CI detection and the inference table, merge-base and shallow handling, diff parsing, renames, untracked and binary files, the list parser, the touched filter and anchor rule, opt-out matching, severity to exit code, sorting, which files get scanned.

## rules

`rules` owns ast-grep rules and their examples.

```go
func Load(ctx context.Context, dirs ...string) (*RuleSet, []Fault) // faults non-empty means nil set, never a partial set
func (*RuleSet) Scan(ctx context.Context, files []string) (Scanned, error)
```

A `*RuleSet` exists only if every example behaved, including a rule whose severity is off, so an unverified rule cannot produce a verdict.
Faults: unparseable rule, empty message, failing example, zero rules, a rule id defined in two files.
A fault names the directory it concerns, and a duplicate id names both files. Ids that differ only by case are the same id.
Scan reads only the given files, anchors each match at its first line, and reports comments so `check` never learns a comment marker.

It hides: the location of the ast-grep binary on PATH, the invocation, zero-based to one-based lines, the rule schema, the sidecar example file `rule-tests/<id>-test.yml` with `valid:` and `invalid:` (ast-grep rejects unknown keys inside a rule), and the `.py` filter.

## diag

`diag` is the one ruff-shaped vocabulary for findings, opt-outs, legacy matches, rule faults and june's own failures.

```go
type Diagnostic struct{ File string; Line, Col int; Code string; Sev Severity; Kind Kind; Msg string }
func Render(ds []Diagnostic, summary string, f Format) (out []byte, files []File) // Format: Text | GitHub | GitLab
```

It hides workflow-command escaping, GitLab Code Quality fingerprints and severities, and summary placement.
Output is text by default and carries no ANSI. Rendering escapes control characters in diagnostic fields and summaries as visible escapes (for example, ESC as `\x1b` and tab as `\t`), preserving other text.

## cmd/june

The current build supports `june --version`, `june verify [--rules DIR]...`, `june [--legacy] [--rules DIR]... [--base REF]` and `june [--legacy] [--rules DIR]... --changed-lines FILE`; `-` reads the list from stdin.
An unstamped `june --version` prints `june dev`.
With ast-grep on PATH, run the shipped Python fundamentals and their sidecar examples from the repository root:

```sh
go run ./cmd/june --rules rules/python --changed-lines changed.txt
```

`rules/python` is the opt-in directory for the shipped fundamentals; adoption is under [Defaults where VISION is silent](#defaults-where-vision-is-silent). The rules are PY012 mutable default argument, PY013 bare `except`, PY014 comparison to `None` with `==` or `!=`, PY015 `except` block that only passes, and PY016 `assert` on a tuple.
Each is an error with a corrective message and its own valid and invalid examples.

The list contains one `path:7` or `path:7-12` entry per line, with one-based, inclusive line ranges and paths relative to the working directory.
Blank lines are ignored; whitespace in filenames is preserved, and only `.py` files are checked.
`--base REF` judges lines added or edited since the merge base with REF, including committed changes, worktree changes and untracked files; the list wins when both are given.
Without either, a bare `june` infers its scope from the environment and git, first hit wins:
- Pre-commit refs: `PRE_COMMIT_FROM_REF` and `PRE_COMMIT_TO_REF`, both set, as in `pre-commit run --from-ref A --to-ref B` and the pre-push stage. The touched lines are those the commits add since the merge base with the from-ref; the worktree and untracked files do not join, and the checkout must be at the to-ref, else exit 2.
- GitHub pull request base: `origin/$GITHUB_BASE_REF` on a `pull_request` or `pull_request_target` run.
- GitLab merge request diff base, described below.
- Pre-commit staged diff: `PRE_COMMIT=1` with no refs. The touched lines are those the index adds against HEAD, or against the empty tree before the first commit, read from the index `GIT_INDEX_FILE` names; unstaged edits and untracked files do not count.
- Local: the upstream branch of HEAD, then the default branch of `origin` (`refs/remotes/origin/HEAD`), with worktree and untracked files.

Nothing is fetched.
Under GitHub Actions a push event has no base: even when the checked-out branch tracks an upstream, which there is HEAD itself, it exits 2 naming `--base REF` with `fetch-depth: 0`, and a pull request whose base was not fetched exits 2 naming `fetch-depth: 0`.
When no base can be inferred, git is missing or the directory is not in a work tree, or the merge base is missing (a shallow clone), june exits 2 naming the fix; the touched set is never every line.
The check emits plain text. Under GitHub Actions (`GITHUB_ACTIONS=true`) it also emits one workflow command per diagnostic before the text: findings as `::error` or `::warning` annotations, opt-outs and legacy lines as `::notice`, and refusals as `::error`. Annotation paths are relative to the repository root, so they land on the right file under `working-directory:`; the text keeps paths relative to the working directory. The text itself is identical everywhere. Completed checks (exit 0 or 1) end with a summary line, `june: 1 error, 0 warnings, 0 opt-outs; checked 2 files, 5 changed lines`; a git run adds `; base origin/main (upstream branch)`, naming the base and its source (`--base`, `pre-commit refs`, `staged diff`, `merge request diff base`, `pull request base`, `upstream branch` or `remote default branch`), and a supplied list adds none; refusals (exit 2) have no summary.
Coverage counts distinct touched Python files and changed lines, counting overlapping ranges only once; the opt-out count is every opt-out printed on a touched line, honoured or not.
`--legacy` also lists matches on untouched lines of the checked files, one `legacy: file:line:col: CODE message` line each after the findings, and the summary gains `; 2 legacy violations listed`.
Legacy lines never change the exit code, are off by default, and a valid opt-out on the untouched line hides its legacy line.
`june verify` runs every example of every rule and prints `june: verified 5 rules, 135 examples` on success. Any fault exits 2, as it does for a normal run, and each fault is one line naming the rules directory, the rule file, or the sidecar, and, for a failing example, the example.
On a check in a GitLab CI job (`GITLAB_CI=true`) the format is inferred as GitLab, and `cmd/june` writes `Files` beside the text: `gl-code-quality-report.json` in the working directory, an empty array for a clean run and no file for a refusal. `june verify` stays text.
Findings are `major` for an error and `minor` for a warning, opt-outs are `info`, and legacy matches stay out; each fingerprint hashes file, rule, message and the ordinal among identical ones.
GitLab's merge request widget compares this report with the latest one from the target branch, so a job that runs only in merge request pipelines leaves the widget with no baseline; a default-branch run needs `--base`, since a branch pipeline has no merge request base.
A merge request pipeline takes its base after `--base`, pre-commit refs and the GitHub pull request base, and before the pre-commit staged diff and local inference, and names it `merge request diff base` in the summary.
A detached pipeline (`CI_MERGE_REQUEST_DIFF_BASE_SHA` set) uses that sha.
A merged results or merge train pipeline (`CI_MERGE_REQUEST_EVENT_TYPE` of `merged_result` or `merge_train`) uses the merge commit's first parent, `HEAD^1`, resolved to its sha for the summary; `CI_MERGE_REQUEST_TARGET_BRANCH_SHA` is ignored, since on a later merge train car it is the target tip and not the first parent.
A clone too shallow to hold the base exits 2 pointing at `GIT_DEPTH: 0`.
The repo publishes the pre-commit hook `june` in `.pre-commit-hooks.yaml`: `language: system`, `types: [python]` and `pass_filenames: false`, so it runs without filenames, only when Python files are involved, and finds its own scope.

`cmd/june` is a shell and owns no decision.
It maps flags to a `Request`, snapshots the environment, writes `Out` and `Files`, and returns `Exit`.

```
june                          # dev shell; a GitLab check also writes the Code Quality report
june --base main --legacy
june --changed-lines FILE     # "-" reads stdin
june --rules shared --rules .june/rules   # shared rules layered under repo rules
june verify                   # rule author: every example behaves, or refused
```

## Version 2: review (planned)

Nothing in this section is built.
It describes `june review`, the advisory LLM review of taste and design rules from VISION.md.
It adds a fifth module and leaves every statement about `check`, `rules` and `diag` above unchanged.

```
cmd/june -> check  -> rules   (Load, Scan)
                   -> diag    (Render)
         -> review -> check   (Scope)
                   -> diag    (Render)
```

Layout: `internal/review`.

### review

`review` is a deep module.
It decides one review: which touched hunks go to the model, what the model says about them under the user's rule file, and which of that survives as a finding.

```go
func Change(ctx context.Context, env Env, req Request) (Result, error)

type Request struct {
	Rules   string      // path to the rule file, any format the reviewer reads; required
	Paths   []string    // narrows only
	Base    string      // as in check.Request
	Changed io.Reader   // as in check.Request
	Model   string      // closed set: anthropic | openai | claude | codex
	Format  diag.Format // "" = inferred
}
type Result struct{ Exit int; Out []byte; Files []diag.File; Diags []diag.Diagnostic }
```

`Env` is the one `check` defines, so credentials come from `Env.Vars` and nothing reads ambient state.

Invariants:
- Exit is 0 or 2, never 1. A finding, however many, never changes Exit.
- Findings are advisory. They are never errors and never fail or block a build.
- `june review` never runs as part of a bare `june`, and `check` never imports `review`.
- A finding is kept only if the model anchors it on a touched line of a touched file; any other claim is dropped.
- Context around a hunk may be sent to the model, but only touched lines can carry a finding, the same rule `check` applies.
- The rule file is read from the supplied path and is never fetched, cloned, discovered or cached. A missing, empty or unreadable file is Exit 2.
- Model output that does not parse into findings is Exit 2 with the cause, never a silent zero findings.
- A review that cannot run (no credential, adapter failure, cancelled context, touched set unknowable) is Exit 2 with `june: error: <cause>. Fix: <instruction>`, never findings by omission.
- Findings are sorted and carry no clock and no absolute path, but the model is not deterministic, so unlike `check` the same input does not promise the same output.
- The same request gives the same findings only under the replay adapter.

Order: touched set, then read the rule file, then build one prompt per touched file, then the model adapter, then drop findings off the touched lines, then render.

It hides: prompt construction, hunk and context selection, size limits and batching, response parsing, retries and timeouts, and which provider answered.

### Scope reuse

`review` does not find touched lines itself.
`check` exposes the touched-set logic as one function, `check.Scope(ctx, env, Scope) (Touched, error)`, and `check.Change` and `review.Change` both call it.
`Request.Base` and `Request.Changed` keep the meaning and the inference table from `check`, so a review judges the same lines the check judges in a dev shell, pre-commit, GitHub Actions and GitLab CI.
An unknowable touched set is Exit 2, never every line.
Scope inference is hidden once, in `check`.

### Model adapter seam

The model is a real seam inside `review`, with these adapters:
- **anthropic**, API token, from `ANTHROPIC_API_KEY` in `Env.Vars`.
- **openai**, API token, from `OPENAI_API_KEY` in `Env.Vars`.
- **claude**, the Claude command-line tool, run locally as a child process.
- **codex**, the Codex command-line tool, run locally as a child process.
- **replay**, a recorded response keyed by the prompt, used by tests only and not reachable from `Model`.

The interface is one call: a prompt in, text out, with a context that cancels it.
`Request.Model` picks the adapter through a closed switch, and an unknown value is Exit 2.
There is no adapter registry and no plugin load, and a new provider is a release.
The API adapters and the command-line adapters are real adapters that differ in where the credential and the network live, so the seam passes the two-real-adapters rule with room to spare.
A cancelled context kills the child process and abandons the request.

### Rule files

The user supplies one path, `--rules-file PATH`, to a file in any format the reviewer reads, such as Markdown prose, a skill file or plain text.
The file may live anywhere: in the repo, in a checked-out shared repo, or in a CI artifact.
`review` passes its text to the model unparsed, so it owns no rule schema and no rule ids, and a finding names the rule in the model's words.
This is not `rules`: those are ast-grep rules with examples that june verifies, and a review rule file has no examples and no verification.
A guideline a tool can decide still belongs in `rules`, and the review file holds judgment, taste and design only.

### Findings and diag

Findings leave through `diag`, with one new `Kind`, `Review`, beside the existing kinds.
A review diagnostic has the touched file and line, the code `REVIEW`, a warning-level severity that never maps to an exit code, and a message that names the rule the model cited and the corrective instruction.
Text output reads like a check finding and carries no ANSI.
`diag` keeps rendering GitHub and GitLab formats from the same `Diagnostic`, so a CI shows review findings as annotations or Code Quality entries beside the check's.
A summary line names the model adapter and the number of findings, and a review that checked nothing says so.
The `# june: ignore[...]` opt-out names a loaded `rules` id and does not apply to review findings.

### Local and CI

- Locally, any adapter works, and the command-line adapters reuse the login the tool already has.
- In CI, credentials come only from the job's environment through `Env.Vars`, never from a flag or a file, and june never prints one.
- The command-line adapters in CI need the tool installed and logged in on the runner; june assumes neither.
- The API adapters need the network, and june says so in the Exit 2 cause when a call fails.
- Whether a command-line adapter uses the network is the tool's affair, and june does not assume it is offline.
- None of this touches `june`: a bare `june` run has no model and no network in every environment.
- A CI job that wants the review never to stop the pipeline can run it as an allowed-to-fail step, since Exit 2 means june could not review and not that a finding landed.

### Seams and tests

- **Model, real, internal to `review`:** four real adapters plus replay, chosen by a closed switch.
- **Touched lines, shared:** the seam stays in `check`, and `review` crosses `check.Scope`.
- **Output format, shared:** `diag` is unchanged except for the `Review` kind.
- `review.Change` runs scenarios on temp trees with the replay adapter and real git, comparing golden `Out`.
- Scenarios cover a finding on a touched line, a finding the model puts on an untouched line and is dropped, malformed model output, a missing credential, and an unknown `Model`.
- One scenario runs under a dev-shell, GitHub and GitLab environment and asserts the same findings.
- Recordings are committed fixtures, and tests never reach a model.
- A smoke run per real adapter runs only when its credential or tool is present, and never in the default test run.

### House rules in version 2

- `review` earns its place by the deletion test: prompts, parsing, drop-off-touched-lines and the adapter seam vanish into nothing without it.
- The model seam has two real adapters or more, and replay is the test adapter.
- No registry or plugin load: the adapter and provider are a closed switch and a release.
- The rule file is a path the user supplies, never a marketplace, a fetch or a registry.
- The check path stays free of a model, a network and a clock.

## Release

Distribution is Homebrew (`brew install vicolby/tap/june`) and the release binaries attached to each GitHub release, with `checksums.txt`.
There is no pip package and no container image.
Pushing a `v*` tag runs `.github/workflows/release.yml`, which runs GoReleaser (`.goreleaser.yaml`) to build darwin and linux binaries for amd64 and arm64, publish a GitHub release, and write `Formula/june.rb` to `vicolby/homebrew-tap`.
Each archive holds the `june` binary and `rules/`; the formula installs the binary to `bin`, the rules to `share/june/rules`, and depends on Homebrew's `ast-grep`.
The workflow needs the repository secret `HOMEBREW_TAP_TOKEN`, a token with contents write access to the tap.
An archive extracts to `june` and `rules/` with no top-level directory and does not install `ast-grep`; the README covers the install.
The formula takes the Homebrew `ast-grep`, not the version pinned in `.ast-grep-version` for tests.
Check the config with `goreleaser check`; it reports `brews` as deprecated, but that is the only block that generates a Formula, and GoReleaser removes deprecated options only in a major version.

## Seams

- **Touched lines, real, internal to `check`:** two adapters, git and the supplied list.
- The list is also a product feature, so `Request.Changed` is a caller input and not a test hook.
- Pre-commit is covered by inference.
- No patch, staged or fixed-set adapter exists, because the list already carries any scope.
- **Output format, real, in `diag`:** three adapters, text, GitHub and GitLab, chosen by a closed switch.
- **ast-grep, hypothetical:** one engine, VISION refuses a second, no interface.
- **Language, hypothetical:** Python lives in rule data and the `.py` filter.
- A second language is the moment to cut a seam.
- **Network, model, clock:** none on the check path. The planned `review` module owns the model seam; see [Version 2: review (planned)](#version-2-review-planned).

## Tests

- The interface is the test surface.
- `check.Change` runs scenarios on temp trees with real git and real ast-grep, comparing golden `Out`.
- Scenarios use `Changed` when git is not under test.
- One scenario runs under a dev-shell, GitHub and GitLab environment and asserts the same exit and finding diagnostics.
- `rules` runs against fixture rule directories.
- `diag` runs against golden output.
- Three to five smoke runs go through `cmd/june`.
- Tests that reach past an interface are disposable.
- The ast-grep version pinned in `.ast-grep-version` is required on PATH for tests.

## Defaults where VISION is silent

Each is reversible and lives here until changed.
- A finding is judged on its first line.
- The opt-out syntax is as above.
- The default rules directory is `./.june/rules`, used when no `--rules` is given.
- `--rules` repeats, and the directories merge into one rule set, so a team can layer bundled, shared and repo-local rules.
- Shared rules ship in the release archive under `rules/` and install to `<prefix>/share/june/rules`. They are opt-in: a repo copies one language directory, such as `python`, to `.june/rules` or passes `--rules`, and june never loads them implicitly.
- Shared rules are supplied by directory path; june never fetches, clones or updates them, and CI checks them out.
- A rule id defined in two files is a rule fault, exit 2 naming both files, in either directory order. A successful verdict does not depend on that order.
- A directory named twice counts once, after cleaning and symlink resolution, and an empty `--rules` value means the default directory.
- Every missing or empty directory is reported, and one fault anywhere refuses the whole run.
- One rule fault refuses the whole run.
- A shallow clone or a push event with no base exits 2 with a named fix.
- Legacy listing is off unless asked.
- Rules are verified on every run, at the cost of one ast-grep process.

## House rules

- A module earns its place by the deletion test: if complexity vanishes without it, it is a pass-through.
- A seam needs two real adapters.
- No pass-through packages.
- Python knowledge stays in rule data.
- No registry or plugin load: extension is a closed switch and a release.
