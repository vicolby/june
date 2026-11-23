# Writing rules

june runs [ast-grep](https://ast-grep.github.io) rules. Each rule is a YAML file plus examples that june verifies before it judges any code. This page is the authoring loop; the contract for what a verdict means is in the [README](../README.md#rules).

## Scaffold

```sh
june new MY001                  # writes .june/rules/MY001.yml and .june/rules/rule-tests/MY001-test.yml
june new --rules rules MY001    # or into another rules directory
```

The scaffold is a rule that already verifies (it flags `print(...)`): edit the pattern, the message and the examples together. `june new` never overwrites a file, and an id may use letters, digits, `-` and `_`.

## Anatomy of a rule

```yaml
id: MY001
language: python
severity: error          # error fails the check; warning and hint do not
message: "Do not call print in library code. Fix: log with the logging module."
note: Printed output is lost under a service manager.   # optional
url: https://example.com/guidelines/logging             # optional
rule:
  pattern: print($$$ARGS)
```

- `message` is the corrective instruction: the person or agent reading a CI failure only has this line. Say what to do, not only what is wrong.
- `note` and `url` are optional strings. They print under the finding (`  note: ...`, `  url: ...`) and travel into every format: `note` and `url` in JSON, the rule's `help` and `helpUri` in SARIF, the annotation message on GitHub, `content.body` in the GitLab report. Empty or whitespace-only values are omitted.
- `files` and `ignores` scope a rule by path. Keys june does not use (such as `fix` or `labels`) are refused by name, so nothing is dropped silently.

The examples sit in `rule-tests/MY001-test.yml`: every `invalid` snippet must be reported by the rule and every `valid` snippet must not be.

```yaml
id: MY001
valid:
  - logging.info("ready")
invalid:
  - print("ready")
```

## Shared utilities

A file in `utils/` beside the rule files defines a global utility rule that every rule of every `--rules` directory can use with `matches`. A utility needs an `id`, a `language` and a `rule`; it is not a rule that june runs, so it has no examples of its own, and the examples of the rules that use it verify it.

```yaml
# rules/utils/is-print.yml
id: is-print
language: python
rule:
  pattern: print($$$ARGS)
```

```yaml
# rules/MY002.yml
id: MY002
language: python
severity: error
message: "Do not call print. Fix: use logging."
rule:
  matches: is-print
```

A utility id defined twice, in the same or in two directories, is a fault.

## Verify one rule

```sh
june verify --rule MY001                 # only the examples of MY001
june verify --rule MY001 --format json   # the same, machine-readable
june verify                              # every rule, as CI does
```

Every rule file is still read, so a broken file anywhere refuses the run, but only the named rule's examples run. A failing example is reported by its number in the `valid` or `invalid` list. `--format` takes `text`, `json`, `sarif` or `github` (`gitlab` prints text, since verify has no findings). In JSON a failure names the examples file and the rule, so a tool does not have to parse the sentence. A success has an empty `diagnostics` list and the summary.

## Worked example: a model drafts a rule

A model is asked for a rule from this guideline: "library code must not call print". It drafts the rule and examples, then loops on `june verify --rule NOPRINT --format json` until it exits 0.

The first draft matches one argument only (`print($ARG)`), but an example has two:

```json
{
  "version": 1,
  "diagnostics": [
    {
      "kind": "failure",
      "severity": "error",
      "file": ".june/rules/rule-tests/NOPRINT-test.yml",
      "rule": "NOPRINT",
      "message": ".june/rules/rule-tests/NOPRINT-test.yml: rule NOPRINT invalid example #2 \"print(\\\"ready\\\", flush=True)\" is not reported by the rule. Fix: make the rule and its examples in rule-tests/NOPRINT-test.yml agree"
    }
  ],
  "summary": ""
}
```

Exit code 2 and a diagnostic with `file`, `rule` and the example number tell the model exactly what to change: the pattern is too narrow for invalid example 2. It widens `print($ARG)` to `print($$$ARGS)` and runs again:

```text
june: verified 1 rule, 3 examples with ast-grep 0.51.3
```

Exit 0. Then it runs `june verify` for all rules, and `june --base main` to see the rule on a real change. Keep the loop honest by writing the examples before the pattern, and by adding an example for every edge the guideline mentions (a method named `print`, a `print` that is passed as a value).

## Errors you may meet

| Output | Meaning |
| --- | --- |
| `the rules path is a file, not a directory` | `--rules` takes the directory that holds the rule files, not one rule file. |
| `no rule has the id X (rules found: ...)` | `--rule` names a rule id that no loaded rule has. |
| `ast-grep was not found` | Install ast-grep and put it on `PATH`. Not about any rules directory. |
| `rule X has the key "fix", which june does not use` | Remove the key; put the correction in `message`. |

A rules directory outside the working directory is named as you spelled it, absolute if you gave it that way.
