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

## GitHub Actions

On a pull request june judges the lines changed since the pull request base and annotates each finding on the diff, beside the same plain text it prints everywhere.
It needs the base branch in the checkout, so fetch the full history:

```yaml
name: june
on: pull_request

jobs:
  june:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - run: brew install vicolby/tap/june
      - run: june --rules "$(brew --prefix)/share/june/rules/python"
```

On a `push` event there is no pull request base, so june exits 2 and names the fix: pass `--base REF` (for example `--base origin/main`) with `fetch-depth: 0`, or run on `pull_request`.

## Releases

Pushing a `v*` tag publishes a GitHub release and updates the formula in [vicolby/homebrew-tap](https://github.com/vicolby/homebrew-tap).
The release workflow needs a `HOMEBREW_TAP_TOKEN` repository secret with write access to the tap.
