// Package rules owns ast-grep rules and their examples.
package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	testsDir        = "rule-tests"
	astGrepName     = "ast-grep"
	astGrepFindFix  = "install ast-grep and put it on PATH, for example npm install --global @ast-grep/cli"
	exampleFixHint  = "make the rule and its examples in %s agree"
	pythonExtension = ".py"

	optOutMarker      = "june:"
	suppressionMarker = "ast-grep-ignore"

	// isolatedConfig is the only ast-grep configuration june ever uses, so a run depends on the
	// shipped rules and the touched files and never on an sgconfig.yml found in the working directory.
	isolatedConfig = "ruleDirs: [rules]\ntestConfigs:\n  - testDir: " + testsDir + "\n"
)

// Fault is one reason a rule directory cannot be trusted.
// Path is relative to the rules directory, empty when the fault concerns the directory itself.
type Fault struct {
	Path  string
	Cause string
	Fix   string
}

// RuleSet is a set of rules whose examples all behaved.
type RuleSet struct {
	bin    string
	inline string
	ids    map[string]bool
}

// Match is one rule match anchored at its first line. Line and Col are one-based.
type Match struct {
	File      string
	Line, Col int
	Rule      string
	Severity  string
	Msg       string
}

// Scanned is what a scan of files found.
type Scanned struct {
	Matches []Match
	OptOuts []OptOut
}

// OptOut is one inline opt-out comment, including malformed attempts. Line and Col are one-based.
// Problem is empty for a well-formed opt-out and otherwise says why it suppresses nothing.
type OptOut struct {
	File      string
	Line, Col int
	Rule      string
	Reason    string
	Problem   string
}

// ruleDoc is the part of an ast-grep rule that june reads.
type ruleDoc struct {
	ID       string `yaml:"id"`
	Message  string `yaml:"message"`
	Language string `yaml:"language"`
}

type exampleDoc struct {
	ID      string   `yaml:"id"`
	Valid   []string `yaml:"valid"`
	Invalid []string `yaml:"invalid"`
}

// Load verifies every rule in dir against its sidecar examples.
// Faults non-empty means a nil set, never a partial set.
func Load(ctx context.Context, dir string) (*RuleSet, []Fault) {
	bin, err := exec.LookPath(astGrepName)
	if err != nil {
		return nil, []Fault{{Cause: "ast-grep was not found", Fix: astGrepFindFix}}
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, []Fault{{Cause: "the rules directory does not exist", Fix: "create it or point --rules at an existing rules directory"}}
	}

	files, err := ruleFiles(dir)
	if err != nil {
		return nil, []Fault{{Cause: "the rules directory cannot be read", Fix: "make the rules directory readable"}}
	}
	if len(files) == 0 {
		return nil, []Fault{{Cause: "the rules directory holds no rules", Fix: "add a rule file such as PY012.yml with its rule-tests/PY012-test.yml, or point --rules elsewhere"}}
	}

	var faults []Fault
	var docs []string
	var examples []string
	ids := map[string]bool{}
	for _, name := range files {
		doc, id, fs := readRule(dir, name)
		if id != "" && ids[id] {
			fs = append(fs, Fault{Path: name, Cause: "the rule id is duplicated", Fix: "give every rule a unique id"})
		}
		ids[id] = true
		examples = append(examples, filepath.Join(testsDir, id+"-test.yml"))
		faults = append(faults, fs...)
		docs = append(docs, doc)
	}
	if len(faults) > 0 {
		return nil, faults
	}
	if faults := verify(ctx, bin, dir, files, examples); len(faults) > 0 {
		return nil, faults
	}
	return &RuleSet{bin: bin, inline: strings.Join(docs, "\n---\n"), ids: ids}, nil
}

// Scan runs the rules over the given files and keeps only Python files.
// Matches are anchored at their first line. File is reported as given.
func (rs *RuleSet) Scan(ctx context.Context, files []string) (Scanned, error) {
	var out Scanned
	var config string
	for _, f := range files {
		if filepath.Ext(f) != pythonExtension {
			continue
		}
		source, err := os.ReadFile(f)
		if err != nil {
			return Scanned{}, errors.New("a Python file cannot be read")
		}
		if !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 {
			return Scanned{}, errors.New("a Python file is not UTF-8 text")
		}
		if config == "" {
			dir, err := os.MkdirTemp("", "june-scan-")
			if err != nil {
				return Scanned{}, errors.New("the scan cannot create its configuration")
			}
			defer func() { _ = os.RemoveAll(dir) }() // Best-effort scratch cleanup cannot change the scan result.
			if config, err = writeConfig(dir); err != nil {
				return Scanned{}, errors.New("the scan cannot create its configuration")
			}
		}
		var cs []comment
		if bytes.Contains(source, []byte(optOutMarker)) || bytes.Contains(source, []byte(suppressionMarker)) {
			if cs, err = rs.comments(ctx, config, source); err != nil {
				return Scanned{}, err
			}
		}
		out.OptOuts = append(out.OptOuts, rs.optOuts(f, source, cs)...)
		disableSuppressions(source, cs)
		stdout, err := runASTGrep(ctx, rs.bin, config, source, "scan", "--inline-rules", rs.inline, "--json=stream")
		if err != nil {
			return Scanned{}, err
		}
		dec := json.NewDecoder(bytes.NewReader(stdout))
		for {
			var m struct {
				RuleID   string `json:"ruleId"`
				Severity string `json:"severity"`
				Message  string `json:"message"`
				Range    struct {
					Start struct{ Line, Column int } `json:"start"`
				} `json:"range"`
			}
			if err := dec.Decode(&m); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return Scanned{}, errors.New("ast-grep scan returned unreadable output")
			}
			out.Matches = append(out.Matches, Match{
				File: f, Line: m.Range.Start.Line + 1, Col: m.Range.Start.Column + 1,
				Rule: m.RuleID, Severity: m.Severity, Msg: m.Message,
			})
		}
	}
	return out, nil
}

// writeConfig lays out the isolated ast-grep configuration in dir and returns its path.
func writeConfig(dir string) (string, error) {
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "sgconfig.yml")
	return path, os.WriteFile(path, []byte(isolatedConfig), 0o644)
}

func runASTGrep(ctx context.Context, bin, config string, source []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, append(args, "--config", config, "--stdin", "--color=never")...)
	cmd.Dir = filepath.Dir(config)
	cmd.Stdin = bytes.NewReader(source)
	stdout, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, errors.New("ast-grep could not complete the scan")
		}
	}
	return stdout, nil
}

// comment is a Python comment node. Line and Col are one-based, start and end are byte offsets.
type comment struct {
	line, col, start, end int
}

// comments finds the comment nodes of source, so a marker inside a string literal is never one.
func (rs *RuleSet) comments(ctx context.Context, config string, source []byte) ([]comment, error) {
	stdout, err := runASTGrep(ctx, rs.bin, config, source, "run", "--lang", "python", "--kind", "comment", "--json=stream")
	if err != nil {
		return nil, err
	}
	var cs []comment
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var m struct {
			Range struct {
				ByteOffset struct{ Start, End int }
				Start      struct{ Line, Column int }
			}
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return cs, nil
			}
			return nil, errors.New("ast-grep returned unreadable comment ranges")
		}
		start, end := m.Range.ByteOffset.Start, m.Range.ByteOffset.End
		if start < 0 || end <= start || end > len(source) {
			return nil, errors.New("ast-grep returned invalid comment ranges")
		}
		cs = append(cs, comment{line: m.Range.Start.Line + 1, col: m.Range.Start.Column + 1, start: start, end: end})
	}
}

// disableSuppressions defuses ast-grep's own suppression comments in place, so only june opt-outs suppress.
func disableSuppressions(source []byte, cs []comment) {
	for _, c := range cs {
		text := source[c.start:c.end]
		copy(text, bytes.ReplaceAll(text, []byte(suppressionMarker), []byte("ast_grep_ignore")))
	}
}

// optOuts reads the opt-outs out of the comments of source. A comment is an opt-out when it starts with `june:`.
func (rs *RuleSet) optOuts(file string, source []byte, cs []comment) []OptOut {
	var out []OptOut
	for _, c := range cs {
		text := string(source[c.start:c.end])
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(text, "#")), optOutMarker) {
			continue
		}
		o := OptOut{File: file, Line: c.line, Col: c.col}
		o.Rule, o.Reason, o.Problem = rs.parseOptOut(text)
		out = append(out, o)
	}
	return out
}

const optOutSyntax = "write # june: ignore[RULE] reason"

func (rs *RuleSet) parseOptOut(text string) (rule, reason, problem string) {
	rest, ok := strings.CutPrefix(text, "# june: ignore[")
	if !ok {
		return "", "", "the comment must start with `# june: ignore[`. Fix: " + optOutSyntax
	}
	rule, reason, ok = strings.Cut(rest, "]")
	if !ok {
		return "", "", "the rule bracket is not closed. Fix: " + optOutSyntax
	}
	switch {
	case rule == "":
		return "", "", "no rule is named. Fix: " + optOutSyntax
	case rule != strings.TrimSpace(rule):
		return "", "", "the rule must not have surrounding whitespace. Fix: " + optOutSyntax
	case strings.ContainsAny(rule, ", \t"):
		return "", "", "an opt-out names exactly one rule. Fix: name a single rule in brackets"
	case !rs.ids[rule]:
		return rule, "", "rule " + rule + " does not exist. Fix: name a rule from the rules directory"
	case strings.TrimSpace(reason) == "":
		return rule, "", "the reason is required. Fix: " + optOutSyntax
	case !strings.HasPrefix(reason, " "):
		return rule, "", "a space is required before the reason. Fix: " + optOutSyntax
	}
	return rule, strings.TrimSpace(reason), ""
}

// ruleFiles lists the rule files directly inside dir, sorted.
func ruleFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// readRule returns the rule text and the faults that make it unusable.
func readRule(dir, name string) (string, string, []Fault) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", "", []Fault{{Path: name, Cause: "the rule cannot be read", Fix: "make the rule file readable"}}
	}
	var doc ruleDoc
	if err := decodeDocument(raw, &doc); err != nil {
		return "", "", []Fault{{Path: name, Cause: "the rule must be one valid YAML document", Fix: "keep exactly one valid YAML document in the rule file"}}
	}
	if doc.ID == "" {
		return "", "", []Fault{{Path: name, Cause: "the rule has no id", Fix: "add an id such as PY012"}}
	}
	if doc.ID == "." || doc.ID == ".." || strings.ContainsAny(doc.ID, "/\\") {
		return "", "", []Fault{{Path: name, Cause: "the rule id is not a filename", Fix: "use a rule id without path separators"}}
	}
	if !strings.EqualFold(doc.Language, "python") {
		return "", "", []Fault{{Path: name, Cause: "the rule language is not Python", Fix: "set language to python"}}
	}
	if strings.TrimSpace(doc.Message) == "" {
		return "", "", []Fault{{Path: name, Cause: fmt.Sprintf("rule %s has an empty message", doc.ID), Fix: "write the corrective instruction in the message"}}
	}

	sidecar := filepath.Join(testsDir, doc.ID+"-test.yml")
	rawEx, err := os.ReadFile(filepath.Join(dir, sidecar))
	if err != nil {
		return "", "", []Fault{{Path: name, Cause: fmt.Sprintf("rule %s has no examples", doc.ID), Fix: fmt.Sprintf("add %s with valid and invalid examples", sidecar)}}
	}
	var ex exampleDoc
	if err := decodeDocument(rawEx, &ex); err != nil {
		return "", "", []Fault{{Path: sidecar, Cause: "the examples must be one valid YAML document", Fix: "keep exactly one valid YAML document in the examples file"}}
	}
	if ex.ID != doc.ID {
		return "", "", []Fault{{Path: sidecar, Cause: "the examples id does not match the rule id", Fix: "set the examples id to " + doc.ID}}
	}
	if len(ex.Valid) == 0 || len(ex.Invalid) == 0 {
		return "", "", []Fault{{Path: sidecar, Cause: fmt.Sprintf("rule %s needs both valid and invalid examples", doc.ID), Fix: "add at least one of each"}}
	}
	return strings.TrimRight(string(raw), "\n"), doc.ID, nil
}

func decodeDocument(raw []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one YAML document")
	}
	return nil
}

var (
	failLine  = regexp.MustCompile(`(?m)^FAIL (\S+)`)
	parseLine = regexp.MustCompile(`Cannot parse rule (\S+)`)
)

// verify runs the examples of every rule through ast-grep in a scratch copy of the directory.
func verify(ctx context.Context, bin, dir string, files, examples []string) []Fault {
	scratch, err := os.MkdirTemp("", "june-rules-")
	if err != nil {
		return []Fault{{Cause: "the examples cannot be run", Fix: "make the temporary directory writable"}}
	}
	defer func() { _ = os.RemoveAll(scratch) }() // Best-effort scratch cleanup cannot change verification.
	if real, err := filepath.EvalSymlinks(scratch); err == nil {
		scratch = real
	}

	if err := stage(scratch, dir, files, examples); err != nil {
		return []Fault{{Cause: "the examples cannot be run", Fix: "make the rules directory readable"}}
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "test", "--config", filepath.Join(scratch, "sgconfig.yml"), "--skip-snapshot-tests", "--color=never")
	cmd.Dir = scratch
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return []Fault{{Cause: "the examples were cancelled", Fix: "run june again"}}
	}

	text := strings.ReplaceAll(stdout.String()+"\n"+stderr.String(), scratch+string(filepath.Separator), "")
	var faults []Fault
	for _, m := range failLine.FindAllStringSubmatch(text, -1) {
		faults = append(faults, Fault{
			Path:  filepath.Join(testsDir, m[1]+"-test.yml"),
			Cause: fmt.Sprintf("an example of rule %s misbehaves (%s)", m[1], caseDetails(text)),
			Fix:   fmt.Sprintf(exampleFixHint, filepath.Join(testsDir, m[1]+"-test.yml")),
		})
	}
	if len(faults) > 0 {
		return faults
	}
	if m := parseLine.FindStringSubmatch(text); m != nil {
		path := strings.TrimPrefix(m[1], "rules"+string(filepath.Separator))
		return []Fault{{Path: path, Cause: "ast-grep cannot parse the rule (" + oneLine(failureReason(text)) + ")", Fix: "fix the rule so ast-grep accepts it"}}
	}
	return []Fault{{Cause: "the examples cannot be run", Fix: "run ast-grep test on the rules directory to see why"}}
}

// stage lays rule files and examples out the way ast-grep test expects them.
func stage(scratch, dir string, files, examples []string) error {
	if _, err := writeConfig(scratch); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(scratch, testsDir), 0o755); err != nil {
		return err
	}
	for _, name := range files {
		if err := copyFile(filepath.Join(dir, name), filepath.Join(scratch, "rules", name)); err != nil {
			return err
		}
	}
	for _, name := range examples {
		if err := copyFile(filepath.Join(dir, name), filepath.Join(scratch, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// caseDetails extracts the failing example report from ast-grep test output.
func caseDetails(text string) string {
	const marker = "-- Case Details -----------"
	_, after, ok := strings.Cut(text, marker)
	if !ok {
		return oneLine(text)
	}
	block, _, _ := strings.Cut(after, "\nFAIL ")
	return oneLine(block)
}

// failureReason picks the innermost cause out of ast-grep's error output.
func failureReason(text string) string {
	var reason string
	for _, l := range strings.Split(text, "\n") {
		if _, after, ok := strings.Cut(l, "╰▻ "); ok {
			reason = after
		}
	}
	if reason == "" {
		return text
	}
	return reason
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
