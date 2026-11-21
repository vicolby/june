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

## Releases

Pushing a `v*` tag publishes a GitHub release and updates the formula in [vicolby/homebrew-tap](https://github.com/vicolby/homebrew-tap).
The release workflow needs a `HOMEBREW_TAP_TOKEN` repository secret with write access to the tap.
