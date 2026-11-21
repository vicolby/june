# june

june enforces code-guideline rules on the lines a change touches. See [VISION.md](VISION.md) for the why and [ARCHITECTURE.md](ARCHITECTURE.md) for the design.

## Install

```sh
brew install vicolby/tap/june
```

This installs the `june` binary, `ast-grep` as a dependency, and the bundled rules.

## Bundled rules

Bundled rules are opt-in: june never loads them on its own.
They install to `$(brew --prefix)/share/june/rules`, one directory per language.

```sh
june verify --rules "$(brew --prefix)/share/june/rules/python"
june --rules "$(brew --prefix)/share/june/rules/python" --changed-lines changed.txt
```

To adopt them in a repo, copy `$(brew --prefix)/share/june/rules/python` to `.june/rules`, the default rules directory.

## GitLab CI

In a merge request pipeline, june judges the lines this merge request changed and writes a GitLab Code Quality report, `gl-code-quality-report.json`, beside the plain text.
A detached pipeline takes the merge request diff base (`CI_MERGE_REQUEST_DIFF_BASE_SHA`).
A merged results or merge train pipeline checks out a merge commit, so it takes that commit's first parent (`CI_MERGE_REQUEST_TARGET_BRANCH_SHA`, or `HEAD^1` when the variable is unset).
The findings and the exit code match any other run of those same lines.
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
Each fingerprint hashes the file, rule, message and the ordinal among identical findings, so moving a finding to another line keeps it.

## Releases

Pushing a `v*` tag publishes a GitHub release and updates the formula in [vicolby/homebrew-tap](https://github.com/vicolby/homebrew-tap).
The release workflow needs a `HOMEBREW_TAP_TOKEN` repository secret with write access to the tap.
