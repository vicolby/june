# Compatibility

What june promises not to change, what it may change, and how a change is announced. VISION.md and [ARCHITECTURE.md](ARCHITECTURE.md) say what june is; this page says what you can build on.

june is at 0.x. Until 1.0, anything below can change in a minor release, and the release notes name the change. From 1.0, the sections marked **Stable** change only in a major release.

## Exit codes (Stable)

| Code | Meaning |
| --- | --- |
| `0` | No error-severity finding on a touched line. |
| `1` | An error-severity finding on a touched line with no valid opt-out. |
| `2` | No verdict: bad usage, bad rules, no base, shallow clone, missing or unsupported `ast-grep`. |

No other code is used for a verdict. Warnings, `--legacy` lines and opt-outs never change the code. A run that cannot give a verdict exits 2 and never 0.

## Summary line (Stable)

Every verdict ends with one line:

```text
june: <N> error[s], <N> warning[s], <N> opt-out[s]; checked <N> file[s], <N> changed line[s][; <coverage notes>][; base <label> (<source>)]
```

The base suffix appears only for Git-derived scope; `--changed-lines` omits it. The prefix up to `changed line[s]` and the order of its fields are stable, so a script can read the counts. The coverage notes, the base label and its source are for people: their wording can change in any minor release, and a script must not match on them. A refusal has no summary line; it is one `june: error: <cause>. Fix: <instruction>` line.

## Output formats

- **`json` (Stable).** One document: `version` (an integer, `1`), `diagnostics`, `summary`. Each diagnostic has `kind` (`finding`, `opt-out`, `legacy`, `failure`), `severity`, `message` and, when they apply, `file`, `line`, `column`, `rule`, `note`, `url`. Fields are only added, never renamed or removed, within a `version`; a breaking change raises `version`. Read unknown fields as absent.
- **`gitlab` Code Quality report (Stable).** `gl-code-quality-report.json` in the working directory: a JSON array of `description`, `check_name` (the rule id), `fingerprint`, `severity` and `location.path` plus `location.lines.begin`. Severity is `major` for an error, `minor` for a warning and `info` for an opt-out. The fingerprint hashes the file, rule, message and the finding's ordinal among identical ones, so it survives edits that only move a finding to another line and GitLab can match it between pipelines. `content.body` carries a rule's `note` and `url` and may be reworded.
- **`sarif`.** SARIF 2.1.0 with `ruleId`, `level`, a location and `partialFingerprints`. The SARIF schema is stable; which notes and help text june adds around it is not.
- **`github`.** `::error`, `::warning` and `::notice` workflow commands with `file`, `line`, `col` and `title` (the rule id), plus a job summary table when `GITHUB_STEP_SUMMARY` is set. The command syntax is GitHub's; the table layout can change.
- **`text`.** `path:line:col: RULE message`, one per finding, sorted. It is meant for people. Parse `json` instead.

Output is sorted and carries no clock and no ANSI, so the same input gives the same bytes.

## Flags and commands (Stable)

`june`, `june verify`, `june new` and `june --version` and their flags are listed in the [README](README.md#usage). A flag is not renamed or removed, and its meaning does not change, within a major version. New flags and new values of `--format` can be added in a minor release. `june --help` output is for people and can change.

## Environment variables

Read as documented, and stable:

| Variable | Read for |
| --- | --- |
| `GITHUB_ACTIONS`, `GITHUB_EVENT_NAME`, `GITHUB_EVENT_PATH`, `GITHUB_BASE_REF`, `GITHUB_STEP_SUMMARY` | Detecting GitHub Actions, the pull request or merge group base, and the job summary. |
| `GITLAB_CI`, `CI_MERGE_REQUEST_EVENT_TYPE`, `CI_MERGE_REQUEST_DIFF_BASE_SHA` | Detecting GitLab CI and the merge request base. |
| `PRE_COMMIT`, `PRE_COMMIT_FROM_REF`, `PRE_COMMIT_TO_REF` | The pre-commit scope. |

The install script reads `JUNE_VERSION`, `JUNE_INSTALL_DIR`, `JUNE_OS`, `JUNE_ARCH`, `JUNE_INSTALL_AST_GREP` and `JUNE_RELEASES_URL` ([README](README.md#install)). june does not read any other variable of its own. It drops the git variables that change what a diff means (`GIT_DIFF_OPTS`, `GIT_EXTERNAL_DIFF` and the `GIT_*_PATHSPECS` family) before it runs git. How june infers the base from these is covered by the scope order in the [README](README.md#usage); a vendor adding a variable never changes a verdict.

## Rules layout (Stable)

- `.june/rules/<id>.yml`: one ast-grep rule per file, `language: python`. `--rules DIR` replaces the directory and may repeat.
- `.june/rules/rule-tests/<id>-test.yml`: the rule's examples, with `valid` and `invalid` lists, checked before any verdict.
- `.june/rules/utils/*.yml`: shared utility rules, used with `matches`.
- `.june/config.yml`: one key, `exclude`, a list of globs.

The keys june reads from a rule (`id`, `language`, `severity`, `message`, `note`, `url`, `rule`, `constraints`, `utils`, `transform`, `rewriters`, `files`, `ignores`) and from an example file (`id`, `valid`, `invalid`) are stable. A key june does not use is refused by name, and june can start to use a key in a minor release, so a refused key is never silently ignored later. A rule set that verifies keeps verifying.

## Rule ids (Stable)

A rule id is the contract between a rule file, its sidecar, an opt-out comment, a finding and a report. june never renames one and never reuses one for a different meaning. An id is a letter or digit followed by letters, digits, `-` or `_`, is unique ignoring case, and `june-parse-error` is reserved for the syntax-error scan. Ids in your rules directory are yours: renaming one breaks the opt-outs and the report fingerprints that name it, so retire an id instead of reusing it.

## Opt-outs (Stable)

`# june: ignore[RULE] reason`, naming exactly one loaded rule with a non-blank reason, covers one finding on its line. The syntax and what a malformed comment does are in the [README](README.md#opt-outs).

## Tool versions

june needs `ast-grep` 0.51.1 or newer (`MinVersion` in `internal/rules/version.go`) and git. Each release names the ast-grep version it was tested with, and CI runs the suite against the floor and the newest release every week. Raising the floor is a minor release before 1.0 and a major release after. A new ast-grep release that changes what a rule matches is not a june change; the pin in `.ast-grep-version` is where june moves to it.

## What 1.0 freezes

At 1.0 the **Stable** items above are frozen until 2.0: the exit codes, the summary line's counts, the `json` document and the Code Quality report fields, the flags and commands, the environment variables june reads, the rules directory layout and sidecar keys, rule-id behaviour and the opt-out syntax. Not frozen: the wording of messages, notes and coverage notes, `text` layout, `--help`, the GitHub job summary table, what a refusal says after `Fix:`, the ast-grep floor between majors only if it is not raised, and anything under `internal/`, which is not an API.

## How the promise is checked

Each stable surface has a test: the exit codes and summary in `internal/check`, the formats in `internal/diag`, rule layout and sidecars in `internal/rules`. CI runs the suite with the race detector on Linux and macOS, runs the parsers' fuzz targets weekly, and runs the binary on a real GitHub Actions runner against a fixture repository, reading the annotations back through the API (`testdata/e2e/run.sh github`).

The automated june CI check for GitLab simulates merge request pipeline variables (`testdata/e2e/run.sh gitlab`). At each release bump, manually run a real GitLab pipeline in the `gimmefear/test-june` project using the release being checked. Open a merge request that adds a mutable default argument and confirm the job fails with exit 1 and the merge request shows one Code Quality finding for `PY012`. This real GitLab check is manual, not an automated june CI job.
