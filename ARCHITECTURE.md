# Architecture

This document applies VISION.md. Where the two disagree, VISION.md wins.
It holds the module map and the invariants. Everything else is in the code and its tests; the package comments and `june --help` are the reference for signatures, flags and formats.

## Modules

```
cmd/june -> check -> rules   (load and verify rules, scan files)
                  -> review  (ask a model about the touched lines; june review only)
                  -> diag    (render diagnostics)
cmd/june -> openai           (the one adapter of review.Model that opens a connection)
```

Layout: `cmd/june`, `internal/{check,rules,review,openai,diag}`, plus `internal/install`. Tests keep fixture rules in `testdata/rules` and `internal/rules/testdata`; june ships no rules.

- `cmd/june` is a shell and owns no decision. It maps flags to a `check.Request`, snapshots the environment, writes the output and report files, and returns the exit code.
- `check` is the deep module. It decides one change: which lines are touched, which findings land on them, which opt-outs apply, and the exit code. It hides repo discovery, CI detection and scope inference, merge-base and shallow-clone handling, diff parsing, the changed-lines list parser, opt-out matching and sorting.
- `rules` owns ast-grep rules, their sidecar examples (`rule-tests/<id>-test.yml`), the shared `utils/` rules and the `june new` scaffold. A rule set loads only if every example behaves, so an unverified rule never produces a verdict. It checks the ast-grep version before verifying examples, stages files at their real paths so a rule's `files` and `ignores` apply, refuses rule keys june does not use (`note` and `url` are used: they travel with each finding), runs ast-grep and hides the invocation, source encodings, and the zero-based to one-based line shift. `RuleSet.Version` is the version a verdict used.
- `review` is the deep module of `june review`. It decides what a model is asked and what of its answer becomes a finding: windows of the touched lines, the instruction and the output contract, hard budgets, and a guard that drops findings off the touched lines, duplicates, and anything past the per-call cap. Its one seam is `review.Model`, with the OpenAI adapter in `openai` and a scripted fake in `review/reviewtest`. Model text reaches output only through `diag`, as a warning. `check.Review` reuses the scope of a change check, so both commands judge the same lines.
- `openai` is the Responses API adapter: strict JSON-schema output, `store: false`, bounded retries, no redirects. It is the only package that imports `net/http`, and `cmd/june` wires it in. The key is never put in an error or a log. A test fails the build if `check`, `rules`, `diag` or `review` ever depend on `net` or `net/http`.
- `diag` is the one ruff-shaped vocabulary for findings, opt-outs, legacy matches, rule faults and june's own failures, and renders it as text, GitHub, GitLab, JSON or SARIF. `june verify` reuses it: a failing example is a failure that carries its file and rule.

## Invariants

- Path exclusions follow the [exclusion contract](README.md#excluding-paths); positional narrowing and project defaults follow [Usage](README.md#usage) and [Config file](README.md#config-file).
- Exit 1 if and only if an error-severity match is anchored on a touched line with no valid opt-out. Warnings, legacy matches and opt-outs never change the exit code.
- Exit 2 means june could not give a verdict (no base, shallow clone, bad rules, missing ast-grep, bad list, unwritable report). Output reads `june: error: <cause>. Fix: <instruction>`.
- A touched line is one added or edited in the post-image. With `--ignore-whitespace`, a line whose only change is whitespace (`git diff -w`) is not touched; a line with any other edit still is. Pure deletions, mode changes and pure renames touch nothing. A new file touches every line, including an untracked file in a local run.
- A match is judged on its first line only, so rules match the smallest wrong node.
- Source files follow the [source-encoding contract](README.md#source-encoding).
- Coverage reporting follows the [coverage-notes contract](README.md#coverage-notes).
- If the touched set is unknowable, the exit is 2, never 0 and never every line. GitHub push base requirements follow the [CI contract](README.md#github-actions).
- Opt-out syntax, suppression limits and visibility follow the [opt-out contract](README.md#opt-outs).
- The same input yields the same exit code and the same findings; CI event context is part of that input (see [GitHub Actions](README.md#github-actions)). Output paths follow [Usage](README.md#usage). The summary's base label names the base and where it came from.
- Output is sorted, with no clock and no ANSI. The safe.directory fix may name the repository path.
- A rule fault anywhere refuses the whole run, never a partial rule set. An unsupported or unreadable ast-grep version is a rule fault.
- Nothing is fetched. The check path (`june`, `june verify`, `june new`) uses no network, model or clock.
- `june review` is the only command that opens a connection, and only to `https://api.openai.com/v1`. Its findings are warnings on touched lines and never change the exit code, and it exits 2 outside GitHub Actions and GitLab CI. The determinism invariants above do not apply to the model's answer.

## Where things are

- Scope inference order: [README.md](README.md#usage). CLI contract: `cmd/june/main.go` (`june --help`).
- Output formats: `internal/diag`.
- Behavior by example: the scenario tests in `internal/check` (real git, real ast-grep), plus `internal/rules` and `internal/diag` tests.
- ast-grep compatibility and the tested pin: `internal/rules/version.go`, `.ast-grep-version`, and [README.md](README.md#install). Scheduled floor/latest suite runs: `.github/workflows/ast-grep-canary.yml`.
- Install and release: `install.sh`, `.goreleaser.yaml`, `.github/workflows/release.yml`, and [README.md](README.md#install). GitHub Action: `action.yml` and [usage](README.md#github-actions).

## House rules

- A module earns its place by the deletion test: if complexity vanishes without it, it is a pass-through.
- A seam needs two real adapters. Touched lines (git and the supplied list), output format and the review model (OpenAI and the scripted fake) are real seams; ast-grep and language are not.
- Guideline-specific Python knowledge stays in rule data.
- No registry or plugin load: extension is a closed switch and a release.
