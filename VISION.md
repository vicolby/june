# Vision

june exists so that code-guideline rules a tool can decide are decided by a tool, never by an LLM's judgment.
It serves teams whose AI agents write code against shared guideline skills, and it turns each checkable rule into a deterministic CI and pre-commit failure that an agent fixes the way it fixes a ruff or mypy error.
It owns exactly one thing: deterministic enforcement of guideline rules on the lines a change touches.

## The skill holds only what a tool cannot check
Every rule that can be decided from a single file's syntax tree belongs in june, and the skill keeps only judgment, taste, and design guidance.
A rule is never maintained twice, once as prose and once as code.
june never reads guideline prose and never judges what a rule means.
A rule that needs the import graph or any cross-file knowledge stays in the skill or in the tool built for it.

## Existing code never excuses a new violation
Agents imitate the code in front of them, so legacy violations teach new ones.
june fails exactly the lines a change adds or edits, never the enclosing function or file, and no rule widens that scope.
An old violation on an untouched line never fails a build.
A touched line that violates a rule always fails, however many neighbors do the same.
june can list old violations on untouched lines as information, and that list never fails or blocks anything.
A repo adopts june with no cleanup and no baseline file to maintain.

## Failures read like ruff
A finding names the file, line, rule id, and the corrective instruction.
An agent fixes it with no human translation, the same way it fixes ruff or mypy output.
june fixes nothing itself, so the agent always sees what was wrong.
A rule has a severity, and only an error fails the check, so a rule that is right most of the time can still ship as a warning.
A line can opt out of one named rule with an inline comment that carries a written reason.
june prints every opt-out a change adds, so a reviewer sees them in the log.
june runs in GitHub Actions and GitLab CI and as a pre-commit hook, with the same verdict everywhere.
In each CI it also reports findings in that CI's native format, beside the plain text.
The same input gives the same verdict on every run, with no network and no model.

## Rules are written once by a model and verified by examples
An LLM drafts a rule from guideline prose at authoring time, and june itself never calls a model.
Every rule ships with passing and failing examples, and june refuses a rule whose examples do not behave.
Rules are ast-grep rules, and june owns them directly, never as configuration for another linter.

## Scope
june is not a linter engine, not a replacement for ruff or mypy, not a rule marketplace, and not an LLM reviewer.
Version 1 checks Python only.
It ships as a pip package and a container image, each with ast-grep bundled.
june finds the touched lines itself from git against the target branch, and also accepts a list of changed lines from the CI job or the pre-commit hook.
Reviewing the guidelines no tool can check, and reporting per-guideline coverage, belong to a later version.

A change aligns when it makes a tool-decidable rule more exact, easier for an agent to act on, or cheaper to adopt on legacy code.
A change should be resisted when it puts a model call on the check path, fails lines the change did not touch, rewrites code on the agent's behalf, or adds a second rule engine or a rule registry.
