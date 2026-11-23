package rules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ruleIDSyntax is the shape of a rule id june scaffolds: it is safe as a filename and as a bare YAML scalar.
var ruleIDSyntax = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

const scaffoldRule = `id: %[1]s
language: python
severity: error
message: "Do not call print. Fix: log with the logging module instead."
rule:
  pattern: print($$$ARGS)
`

const scaffoldExamples = `id: %[1]s
valid:
  - |
    logging.info("ready")
invalid:
  - |
    print("ready")
`

// Scaffold writes a minimal rule and its examples for id into dir, creating dir when it does not exist, and returns the two paths relative to dir.
// The rule passes verification as written. Nothing is overwritten: a rule or examples file that exists is a fault.
func Scaffold(dir, id string) ([]string, *Fault) {
	switch {
	case !ruleIDSyntax.MatchString(id):
		f := general(fmt.Sprintf("%q is not a usable rule id", id), "use letters, digits, - and _, starting with a letter or digit, such as MY001")
		return nil, &f
	case strings.EqualFold(id, parseErrorID):
		f := general(fmt.Sprintf("the rule id %s is reserved", id), "choose another id")
		return nil, &f
	}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return nil, &Fault{Cause: "the rules path is a file, not a directory", Fix: "point --rules at a directory"}
	}
	ruleRel := id + ".yml"
	exampleRel := filepath.Join(testsDir, id+"-test.yml")
	for _, rel := range []string{ruleRel, exampleRel} {
		if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
			return nil, &Fault{Path: rel, Rule: id, Cause: "the file already exists", Fix: "choose another id, or edit the existing rule"}
		}
	}
	files := []struct{ rel, body string }{{ruleRel, fmt.Sprintf(scaffoldRule, id)}, {exampleRel, fmt.Sprintf(scaffoldExamples, id)}}
	var made []string
	for _, f := range files {
		if err := writeNew(filepath.Join(dir, f.rel), []byte(f.body)); err != nil {
			for _, m := range made {
				_ = os.Remove(filepath.Join(dir, m)) // Best-effort cleanup of the half-written scaffold.
			}
			cause := "the rule cannot be written"
			if errors.Is(err, os.ErrExist) {
				cause = "the file already exists"
			}
			return nil, &Fault{Path: f.rel, Rule: id, Cause: cause, Fix: "make the rules directory writable, or choose another id"}
		}
		made = append(made, f.rel)
	}
	return made, nil
}
