# Compatibility

What june promises not to change, what it may change, and how a change is announced. VISION.md and [ARCHITECTURE.md](ARCHITECTURE.md) say what june is; this page says what you can build on.

june is at 0.x. Until 1.0, anything below can change in a minor release, and the release notes name the change. From 1.0, the sections marked **Stable** change only in a major release.

## Exit codes (Stable)

| Code | Meaning |
| --- | --- |
| `0` | No unsuppressed error-severity finding on a touched line. |
| `1` | An error-severity finding on a touched line with no valid opt-out. |
| `2` | No verdict: bad usage, bad rules, no base, shallow clone, missing or unsupported `ast-grep`. |

No other code is used for a verdict. Warnings, `--legacy` lines and opt-outs never change the code. A run of `june`, `june verify` or `june new` that cannot give a verdict exits 2 and never 0. `june review` is the exception: see [below](#june-review-experimental).

## Summary line (Stable)

A change-check verdict carries this summary. It closes the text, GitHub and GitLab output; JSON stores it in `summary`, and SARIF stores it as a tool execution notification:

```text
june: <N> error[s], <N> warning[s], <N> opt-out[s]; checked <N> file[s], <N> changed line[s][; <details>]
```

The prefix up to `changed line[s]` and the order of its fields are stable, so a script can read the counts. Details can include coverage notes, `base <label> (<source>)`, excluded-file counts and legacy counts. The base detail appears only for Git-derived scope; `--changed-lines` omits it. Details are for people: their wording can change in any minor release, and a script must not match on them. Verification has a different summary, described in [Writing rules](docs/writing-rules.md#verify-one-rule).

Rule and scope refusals omit the summary and may report multiple failures. Text failure lines read `june: error: <cause>. Fix: <instruction>`; JSON and SARIF encode failures in their documents. CLI failures, including bad usage and report-write errors, print text to stderr regardless of format. A report-write error can follow an already rendered summary.

## Output formats

- **`json` (Stable).** One document: `version` (an integer, `1`), `diagnostics`, `summary`. Each diagnostic has `kind` (`finding`, `opt-out`, `legacy`, `failure`), `severity`, `message` and, when they apply, `file`, `line`, `column`, `rule`, `note`, `url`. Fields are only added, never renamed or removed, within a `version`; a breaking change raises `version`. Consumers must ignore unknown fields.
- **`gitlab` Code Quality report (Stable).** `gl-code-quality-report.json` in the working directory: a JSON array of `description`, `check_name` (the rule id), `fingerprint`, `severity` and `location.path` plus `location.lines.begin`. Severity is `major` for an error, `minor` for a warning and `info` for an opt-out. The fingerprint hashes the file, rule, message and the finding's ordinal among identical ones, so it survives edits that only move a finding to another line and GitLab can match it between pipelines. `content.body` carries a rule's `note` and `url` and may be reworded.
- **`sarif`.** SARIF 2.1.0 with `ruleId`, `level`, a location and `partialFingerprints`. The SARIF schema is stable; which notes and help text june adds around it is not.
- **`github`.** `::error`, `::warning` and `::notice` workflow commands followed by the plain text output. Located diagnostics have `file`, `line`, `col` and `title` (the rule id); failures have only `title=june`. A job summary table is appended when `GITHUB_STEP_SUMMARY` is set. The command syntax is GitHub's; the table layout can change.
- **`text`.** `path:line:col: RULE message`, one per finding, sorted, with optional indented rule details. It is meant for people. Parse `json` instead.

The GitLab report includes findings and opt-outs, not legacy matches or failures. A clean check writes an empty array; a refusal or `june verify` writes no report. Text, JSON and GitLab paths are relative to the working directory; GitHub annotations and SARIF use repository-root paths when the root is known.

Output is sorted and carries no clock and no ANSI, so the same input gives the same bytes.

## Flags and commands (Stable)

`june`, `june verify`, `june new` and `june --version` and their flags are listed in the [README](README.md#usage). A flag is not renamed or removed, and its meaning does not change, within a major version. New flags and new values of `--format` can be added in a minor release. `june --help` output is for people and can change.

## Environment variables (Stable)

Read as documented, and stable:

| Variable | Read for |
| --- | --- |
| `GITHUB_ACTIONS`, `GITHUB_EVENT_NAME`, `GITHUB_EVENT_PATH`, `GITHUB_BASE_REF`, `GITHUB_STEP_SUMMARY` | Detecting GitHub Actions, the pull request or merge group base, and the job summary. |
| `GITLAB_CI`, `CI_MERGE_REQUEST_EVENT_TYPE`, `CI_MERGE_REQUEST_DIFF_BASE_SHA` | Detecting GitLab CI and the merge request base. |
| `PRE_COMMIT`, `PRE_COMMIT_FROM_REF`, `PRE_COMMIT_TO_REF` | The pre-commit scope. |
| `OPENAI_API_KEY` | `june review` only, and [Experimental](#june-review-experimental), not Stable. |

The install script reads `JUNE_VERSION`, `JUNE_INSTALL_DIR`, `JUNE_OS`, `JUNE_ARCH`, `JUNE_INSTALL_AST_GREP` and `JUNE_RELEASES_URL` ([README](README.md#install)). june does not read any other variable of its own. It drops the git variables that change what a diff means (`GIT_DIFF_OPTS`, `GIT_EXTERNAL_DIFF` and the `GIT_*_PATHSPECS` family) before it runs git. How june infers the base from these is covered by the scope order in the [README](README.md#usage); a vendor adding a variable never changes a verdict.

## `june review` (Experimental)

Nothing in this section is Stable, and 1.0 does not freeze it. It can change in any minor release, and the release notes name the change. Breaking it never breaks the Stable sections: a bare `june`, `verify` and `new` never call a model.

- **Command.** `june review --rules FILE --model MODEL [--effort LEVEL] [--skill PATH | --prompt TEXT]`, with the scope flags of `june` (`--base`, `--changed-lines`, `--exclude`, `--ignore-whitespace`, `--format`, `PATH`). `--rules` is one Markdown file and `--model` is required. `--skill` and `--prompt` together exit 2.
- **Environment.** `OPENAI_API_KEY`. Requests go to `https://api.openai.com/v1`. It reads no other variable of its own.
- **Where.** GitHub Actions and GitLab CI only. Elsewhere, under pre-commit and under `pull_request_target` it exits 2.
- **Exit codes.** Exit 2 for bad usage, a bad rules file, skill or prompt, a scope it cannot establish, and a refused environment. Otherwise 0, always. A finding is a warning with rule `AI`, and a failed call (no key, a rejected key, an unknown model, a timeout) is a loud warning with exit 0, so a green `june review` can mean it could not review. Read the summary's count of files not reviewed.
- **Reports.** The formats of `june`, with `AI` as the rule code and the summary heading `june review`. The GitLab report is `gl-code-quality-review-report.json`, so it never overwrites the check's. Its fingerprint hashes the file and line, because the model's wording varies between runs. The summary ends with an `ai review ...` clause, and its wording is for people.
- **Not promised.** The wording and number of findings, the default instruction, the budgets, the request sent to the API and the set of supported providers. An AI finding has no opt-out.

`june review` has no live test in CI. Its tests use a scripted model and a local fake of the API, and this release was not run against the live OpenAI API. At each release bump that changes it, run it manually once against a pull request with a real key and a cheap model, and confirm a finding appears as an annotation and that a wrong model name gives a warning with exit 0.

## Rules layout (Stable)

- `.june/rules/*.yml` or `*.yaml`: one ast-grep rule per file directly in the directory, `language: python`. The filename need not match the rule id. `--rules DIR` replaces the directory and may repeat.
- `.june/rules/rule-tests/<id>-test.yml`: one YAML document with the rule's exact `id` and non-empty `valid` and `invalid` lists of source snippets. Examples are checked before any verdict; see [Writing rules](docs/writing-rules.md#anatomy-of-a-rule).
- `.june/rules/utils/*.yml` or `*.yaml`: shared utility rules, used with `matches`.
- `.june/config.yml`: optional `rules`, `base` and `exclude` defaults, resolved from the repository root, following the [config file](README.md#config-file) and the [exclusion contract](README.md#excluding-paths). Unknown keys are refused. `june init [--ci github|gitlab]` writes the starter layout and one CI snippet, create-only; see [Setting up](README.md#setting-up). Positional paths (`june [paths]`) narrow a check to files and directories relative to the working directory, spelled as the repository spells them.

The supported rule keys (`usedKeys`) and example fields (`exampleDoc`) are defined in [internal/rules/rules.go](internal/rules/rules.go). Their names and meanings are stable. Rule keys june does not use are refused by name; support for additional keys can be added in a minor release. With the same ast-grep version, a rule set that verifies keeps verifying within a major version.

## Rule ids (Stable)

A rule id is the contract between a rule file, its sidecar, an opt-out comment, a finding and a report. june never renames one and never reuses one for a different meaning. Ids must be unique ignoring case. The `june new` id syntax is documented in [Writing rules](docs/writing-rules.md#scaffold); the reserved syntax-error rule id is documented in the [README](README.md#rules). Ids in your rules directory are yours: renaming one breaks the opt-outs and the report fingerprints that name it, so retire an id instead of reusing it.

## Opt-outs (Stable)

The opt-out syntax, suppression limits and malformed-comment behavior documented in the [README](README.md#opt-outs) are stable.

## Tool versions

Required tools and ast-grep version validation are documented in the [README](README.md#install); the floor is defined by `MinVersion` in `internal/rules/version.go`, and the tested version by `.ast-grep-version`. Each release names the ast-grep version it was tested with, and CI runs the suite against the floor and the newest release every week. Raising the floor is a minor release before 1.0 and a major release after. A new ast-grep release that changes what a rule matches is not a june change; the pin in `.ast-grep-version` is where june moves to it.

## What 1.0 freezes

At 1.0 the **Stable** items above are frozen until 2.0. Not frozen: the wording of messages, notes and coverage notes, `text` layout, `--help`, the GitHub job summary table, what a refusal says after `Fix:`, and anything under `internal/`, which is not an API. The tool-version policy above governs changes to the ast-grep floor.

## How the promise is checked

Each stable surface has a test (Experimental ones, `june review`, have scenario tests with a scripted model and a local fake API, in `internal/check`, `internal/review`, `internal/openai` and `cmd/june`): the exit codes and summary in `internal/check`, the formats in `internal/diag`, rule layout and sidecars in `internal/rules`. CI runs the suite with the race detector on Linux and macOS, runs the parsers' fuzz targets weekly, and runs the binary on a real GitHub Actions runner against a fixture repository, reading the annotations back through the API (`testdata/e2e/run.sh github`).

The automated june CI check for GitLab simulates merge request pipeline variables (`testdata/e2e/run.sh gitlab`). At each release bump, manually run a real GitLab pipeline in the `gimmefear/test-june` project using the release being checked. Open a merge request that adds a mutable default argument and confirm the job fails with exit 1 and the merge request shows one Code Quality finding for `PY012`. This real GitLab check is manual, not an automated june CI job.
