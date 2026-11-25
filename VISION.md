# Vision

june exists so that code-guideline rules a tool can decide are decided by a tool, never by an LLM's judgment.
It serves teams whose AI agents write code against shared guideline skills, and it turns each checkable rule into a deterministic CI and pre-commit failure that an agent fixes the way it fixes a ruff or mypy error.
It owns deterministic enforcement of guideline rules on the lines a change touches.
An advisory LLM review of the taste and design guidelines no tool can decide is a separate, experimental command.

## The skill holds only what a tool cannot check
Every rule that can be decided from a single file's syntax tree belongs in june, and the skill keeps only judgment, taste, and design guidance.
A rule is never maintained twice, once as prose and once as code.
The check never reads guideline prose and never judges what a rule means.
A rule that needs the import graph or any cross-file knowledge stays in the skill or in the tool built for it.

## Existing code never excuses a new violation
Agents imitate the code in front of them, so legacy violations teach new ones.
june fails exactly the lines a change adds or edits, never the enclosing function or file, and no rule widens that scope.
An old violation on an untouched line never fails a build.
A touched line that violates a rule always fails, however many neighbors do the same.
That strictness is the default, and a whitespace-only edit counts as a touch.
An opt-in flag, with `git diff -w` semantics, stops counting a line whose only change is whitespace, so indenting a body into a `with` does not re-blame legacy code.
Wrapping or unwrapping a signature moves code onto another line, so that line stays touched and a legacy violation there still fails.
A line with any other edit is still touched, and the flag never changes what a rule matches.
june can list old violations on untouched lines as information, and that list never fails or blocks anything.
A repo adopts june with no cleanup and no baseline file to maintain.

## Failures read like ruff
A finding names the file, line, rule id, and the corrective instruction.
An agent fixes it with no human translation, the same way it fixes ruff or mypy output.
june fixes nothing itself, so the agent always sees what was wrong.
A rule has a severity, and only an error fails the check, so a rule that is right most of the time can still ship as a warning.
Opt-outs require a written reason and stay visible to reviewers; their syntax and scope follow the [opt-out contract](README.md#opt-outs).
june runs in GitHub Actions and GitLab CI and as a pre-commit hook, with the same verdict everywhere.
In each CI it also reports findings in that CI's native format, beside the plain text.
The same input gives the same verdict on every run, with no network and no model.

## Rules are written once by a model and verified by examples
An LLM drafts a rule from guideline prose at authoring time, and the check never calls a model.
Every rule ships with passing and failing examples, and june refuses a rule whose examples do not behave.
Rules are ast-grep rules, and june owns them directly, never as configuration for another linter.

## Taste and design rules get an advisory LLM review
`june review` is a separate command that asks a model whether the lines a change touches follow the taste and design rules no tool can decide.
The user supplies the rule file by path, from any source.
The first cut is experimental: it runs only in CI, through the OpenAI API with a key from a CI secret, and the user names the model.
Later versions add local use, the Anthropic API and the Claude or Codex command-line tools.
The review is opt-in and never part of a bare `june` run.
Its findings are advisory only: they are warnings, never set exit 1 and never block a build, like `--legacy`.
A review finding never replaces a deterministic rule, and a rule a tool can decide stays in june.
Every v1 guarantee holds on the check path.
The deterministic check never calls a model, has no network and gives the same verdict on every run.

## Scope
june is not a linter engine, not a replacement for ruff or mypy, and not a rule marketplace.
The check path has no model.
The LLM review is a separate command, and its findings never decide a verdict.
Version 1 checks Python only.
june ships no rules: the user supplies their own.
It ships through Homebrew, with ast-grep as a dependency.
It also ships as release binaries for darwin and linux, with ast-grep installed separately.
There is no pip package and no container image.
june finds the touched lines itself from git against the target branch, and also accepts a list of changed lines from the CI job or the pre-commit hook.
Reviewing the guidelines no tool can check is the experimental `june review`; local use and other providers belong to a later version.
Reporting per-guideline coverage belongs to a later version.

A change aligns when it makes a tool-decidable rule more exact, easier for an agent to act on, or cheaper to adopt on legacy code, or when it makes the advisory review more useful to an agent without letting it decide a verdict.
A change should be resisted when it puts a model call on the check path, fails lines the change did not touch, rewrites code on the agent's behalf, makes a review finding fail or block a build, runs the review as part of a bare `june`, or adds a second rule engine or a rule registry.
