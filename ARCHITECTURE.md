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
- `cmd/june` explains cancellation and a working-directory lookup failure as Exit 2 diagnostics, preserving the lookup cause; it reserves the june-bug message for unexplained errors.
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

`rules/python` is the opt-in directory for the shipped fundamentals until a pip package and a container image exist (issues #14 and #15). A repo adopts it by pointing `--rules` at `rules/python` or copying it to `.june/rules`. The rules are PY012 mutable default argument, PY013 bare `except`, PY014 comparison to `None` with `==` or `!=`, PY015 `except` block that only passes, and PY016 `assert` on a tuple.
Each is an error with a corrective message and its own valid and invalid examples.

The list contains one `path:7` or `path:7-12` entry per line, with one-based, inclusive line ranges and paths relative to the working directory.
Blank lines are ignored; whitespace in filenames is preserved, and only `.py` files are checked.
`--base REF` judges lines added or edited since the merge base with REF, including committed changes, worktree changes and untracked files; the list wins when both are given.
Without either, a bare `june` infers the base locally from git, first hit wins: the upstream branch of HEAD, then the default branch of `origin` (`refs/remotes/origin/HEAD`). Nothing is fetched.
When no base can be inferred, git is missing or the directory is not in a work tree, or the merge base is missing (a shallow clone), june exits 2 naming the fix; the touched set is never every line.
The current check emits plain text. Completed checks (exit 0 or 1) end with a summary line, `june: 1 error, 0 warnings, 0 opt-outs; checked 2 files, 5 changed lines`; a git run adds `; base origin/main (upstream branch)`, naming the base and its source (`--base`, `upstream branch` or `remote default branch`), and a supplied list adds none; refusals (exit 2) have no summary.
Coverage counts distinct touched Python files and changed lines, counting overlapping ranges only once; the opt-out count is every opt-out printed on a touched line, honoured or not.
`--legacy` also lists matches on untouched lines of the checked files, one `legacy: file:line:col: CODE message` line each after the findings, and the summary gains `; 2 legacy violations listed`.
Legacy lines never change the exit code, are off by default, and a valid opt-out on the untouched line hides its legacy line.
`june verify` runs every example of every rule and prints `june: verified 5 rules, 103 examples` on success. Any fault exits 2, as it does for a normal run, and each fault is one line naming the rules directory, the rule file, or the sidecar, and, for a failing example, the example.
CI base inference (pre-commit refs, pull and merge request bases, staged diffs) and CI-specific formats remain planned.

`cmd/june` is a shell and owns no decision.
It maps flags to a `Request`, snapshots the environment, writes `Out` and `Files`, and returns `Exit`.

```
june                          # identical in a dev shell, pre-commit, GitHub Actions and GitLab CI
june --base main --legacy
june --changed-lines FILE     # "-" reads stdin
june --rules shared --rules .june/rules   # shared rules layered under repo rules
june verify                   # rule author: every example behaves, or refused
```

## Release

Distribution is Homebrew only: `brew install vicolby/tap/june`.
There is no pip package and no container image.
Pushing a `v*` tag runs `.github/workflows/release.yml`, which runs GoReleaser (`.goreleaser.yaml`) to build darwin and linux binaries for amd64 and arm64, publish a GitHub release, and write `Formula/june.rb` to `vicolby/homebrew-tap`.
Each archive holds the `june` binary and `rules/`; the formula installs the binary to `bin`, the rules to `share/june/rules`, and depends on Homebrew's `ast-grep`.
The workflow needs the repository secret `HOMEBREW_TAP_TOKEN`, a token with contents write access to the tap.
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
- **Network, model, clock:** none exist.

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
