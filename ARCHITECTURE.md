# Architecture

This document applies VISION.md. Where the two disagree, VISION.md wins.
It holds the module map and the invariants. Everything else is in the code and its tests; the package comments and `june --help` are the reference for signatures, flags and formats.

## Modules

```
cmd/june -> check -> rules   (load and verify rules, scan files)
                  -> diag    (render diagnostics)
```

Layout: `cmd/june`, `internal/{check,rules,diag}`, plus `internal/install`, which only tests `install.sh`. Tests keep fixture rules in `testdata/rules` and `internal/rules/testdata`; june ships no rules.

- `cmd/june` is a shell and owns no decision. It maps flags to a `check.Request`, snapshots the environment, writes the output and report files, and returns the exit code.
- `check` is the deep module. It decides one change: which lines are touched, which findings land on them, which opt-outs apply, and the exit code. It hides repo discovery, CI detection and scope inference, merge-base and shallow-clone handling, diff parsing, the changed-lines list parser, opt-out matching and sorting.
- `rules` owns ast-grep rules and their sidecar examples (`rule-tests/<id>-test.yml`). A rule set loads only if every example behaves, so an unverified rule never produces a verdict. It runs ast-grep and hides the invocation, source encodings, and the zero-based to one-based line shift.
- `diag` is the one ruff-shaped vocabulary for findings, opt-outs, legacy matches, rule faults and june's own failures, and renders it as text, GitHub, GitLab, JSON or SARIF.

## Invariants

- Exit 1 if and only if an error-severity match is anchored on a touched line with no valid opt-out. Warnings, legacy matches and opt-outs never change the exit code.
- Exit 2 means june could not give a verdict (no base, shallow clone, bad rules, missing ast-grep, bad list, unwritable report). Output reads `june: error: <cause>. Fix: <instruction>`.
- A touched line is one added or edited in the post-image. Pure deletions, mode changes and pure renames touch nothing. A new file touches every line, including an untracked file in a local run.
- A match is judged on its first line only, so rules match the smallest wrong node.
- Scan reads each file as Python does (PEP 263) and transcodes utf-8, latin-1, ascii and cp1252 to UTF-8, which keeps line numbers. A file it cannot read as text is skipped with a reason and never fails the scan; `check` warns for each and counts them in the summary.
- If the touched set is unknowable, the exit is 2, never 0 and never every line. A GitHub push with no base and no changed-lines list is unknowable even when the branch tracks its remote.
- An opt-out is a same-line Python comment `# june: ignore[RULE_ID] <reason>` naming one loaded rule, with a nonblank reason. A comment starting with `june:` that does not parse suppresses nothing and says why. The same text in a string literal is not an opt-out.
- Every opt-out on a touched line is printed, honoured or not.
- The same touched lines yield the same exit code and the same findings. Each format renders them differently: text, JSON and GitLab paths stay relative to the working directory, GitHub annotations and SARIF use repository-root paths when that root is known, and SARIF omits the summary. The summary's base label names the base and where it came from.
- Output is sorted, with no clock and no ANSI. The safe.directory fix may name the repository path.
- A rule fault anywhere refuses the whole run, never a partial rule set.
- Nothing is fetched. The check path uses no network, model or clock.

## Where things are

- Scope inference order and the CLI contract: `internal/check/git.go`, `cmd/june/main.go` (`june --help`).
- Output formats: `internal/diag`.
- Behavior by example: the scenario tests in `internal/check` (real git, real ast-grep), plus `internal/rules` and `internal/diag` tests.
- Install and release: `install.sh`, `.goreleaser.yaml`, `.github/workflows/release.yml`, and [README.md](README.md#install).

## House rules

- A module earns its place by the deletion test: if complexity vanishes without it, it is a pass-through.
- A seam needs two real adapters. Touched lines (git and the supplied list) and output format are real seams; ast-grep and language are not.
- Python knowledge stays in rule data.
- No registry or plugin load: extension is a closed switch and a release.
