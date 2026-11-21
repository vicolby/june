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

// Place is a file inside one of the rules directories given to Load.
// Dir indexes that argument list and Path is relative to the directory.
type Place struct {
	Dir  int
	Path string
}

// Fault is one reason a rule directory cannot be trusted.
// Dir is the index of the directory it concerns in the list given to Load.
// Path is relative to that directory, empty when the fault concerns the directory itself.
// Also is the other file involved when two files conflict, such as a rule id defined twice.
type Fault struct {
	Dir   int
	Path  string
	Cause string
	Fix   string
	Also  *Place
}

// entry is one rule file waiting for its examples to be verified.
type entry struct {
	place    Place
	id       string
	doc      string
	examples int
}

// RuleSet is a set of rules whose examples all behaved.
type RuleSet struct {
	bin      string
	inline   string
	ids      map[string]bool
	rules    int
	examples int
}

// Rules is the number of rules that were verified.
func (rs *RuleSet) Rules() int { return rs.rules }

// Examples is the number of valid and invalid examples that were verified.
func (rs *RuleSet) Examples() int { return rs.examples }

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

// Load verifies every rule in every dir against its sidecar examples and merges them into one set.
// The same rule id in two files is a fault, wherever the files live. Ids that differ only by case are the same id.
// Faults non-empty means a nil set, never a partial set.
func Load(ctx context.Context, dirs ...string) (*RuleSet, []Fault) {
	bin, err := exec.LookPath(astGrepName)
	if err != nil {
		return nil, []Fault{{Cause: "ast-grep was not found", Fix: astGrepFindFix}}
	}
	if len(dirs) == 0 {
		return nil, []Fault{{Cause: "no rules directory was given", Fix: "point --rules at a rules directory"}}
	}

	var faults []Fault
	var entries []entry
	places := map[string]Place{}
	for i, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory does not exist", Fix: "create it or point --rules at an existing rules directory"})
			continue
		}
		files, err := ruleFiles(dir)
		if err != nil {
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory cannot be read", Fix: "make the rules directory readable"})
			continue
		}
		if len(files) == 0 {
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory holds no rules", Fix: "add a rule file such as PY012.yml with its rule-tests/PY012-test.yml, or point --rules elsewhere"})
			continue
		}
		for _, name := range files {
			doc, id, n, fs := readRule(dir, name)
			for _, f := range fs {
				f.Dir = i
				faults = append(faults, f)
			}
			if id == "" {
				continue
			}
			if first, dup := knownID(places, id); dup {
				faults = append(faults, Fault{Dir: i, Path: name, Cause: fmt.Sprintf("the rule id %s is duplicated", id), Fix: "give every rule a unique id", Also: &first})
				continue
			}
			here := Place{Dir: i, Path: name}
			places[id] = here
			entries = append(entries, entry{place: here, id: id, doc: doc, examples: n})
		}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	if faults := verify(ctx, bin, dirs, entries); len(faults) > 0 {
		return nil, faults
	}
	docs := make([]string, len(entries))
	ids := make(map[string]bool, len(entries))
	total := 0
	for i, e := range entries {
		docs[i] = e.doc
		ids[e.id] = true
		total += e.examples
	}
	return &RuleSet{bin: bin, inline: strings.Join(docs, "\n---\n"), ids: ids, rules: len(entries), examples: total}, nil
}

// knownID reports the file that already owns id. Comparison is case-insensitive because staging uses the id as a filename.
func knownID(places map[string]Place, id string) (Place, bool) {
	for seen, at := range places {
		if strings.EqualFold(seen, id) {
			return at, true
		}
	}
	return Place{}, false
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

// readRule returns the rule text, its id, its example count and the faults that make it unusable.
func readRule(dir, name string) (string, string, int, []Fault) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", "", 0, []Fault{{Path: name, Cause: "the rule cannot be read", Fix: "make the rule file readable"}}
	}
	var doc ruleDoc
	if err := decodeDocument(raw, &doc); err != nil {
		return "", "", 0, []Fault{{Path: name, Cause: "the rule must be one valid YAML document", Fix: "keep exactly one valid YAML document in the rule file"}}
	}
	if doc.ID == "" {
		return "", "", 0, []Fault{{Path: name, Cause: "the rule has no id", Fix: "add an id such as PY012"}}
	}
	if doc.ID == "." || doc.ID == ".." || strings.ContainsAny(doc.ID, "/\\") {
		return "", "", 0, []Fault{{Path: name, Cause: "the rule id is not a filename", Fix: "use a rule id without path separators"}}
	}
	if !strings.EqualFold(doc.Language, "python") {
		return "", "", 0, []Fault{{Path: name, Cause: "the rule language is not Python", Fix: "set language to python"}}
	}
	if strings.TrimSpace(doc.Message) == "" {
		return "", "", 0, []Fault{{Path: name, Cause: fmt.Sprintf("rule %s has an empty message", doc.ID), Fix: "write the corrective instruction in the message"}}
	}

	sidecar := filepath.Join(testsDir, doc.ID+"-test.yml")
	rawEx, err := os.ReadFile(filepath.Join(dir, sidecar))
	if err != nil {
		return "", "", 0, []Fault{{Path: name, Cause: fmt.Sprintf("rule %s has no examples", doc.ID), Fix: fmt.Sprintf("add %s with valid and invalid examples", sidecar)}}
	}
	var ex exampleDoc
	if err := decodeDocument(rawEx, &ex); err != nil {
		return "", "", 0, []Fault{{Path: sidecar, Cause: "the examples must be one valid YAML document", Fix: "keep exactly one valid YAML document in the examples file"}}
	}
	if ex.ID != doc.ID {
		return "", "", 0, []Fault{{Path: sidecar, Cause: "the examples id does not match the rule id", Fix: "set the examples id to " + doc.ID}}
	}
	if len(ex.Valid) == 0 || len(ex.Invalid) == 0 {
		return "", "", 0, []Fault{{Path: sidecar, Cause: fmt.Sprintf("rule %s needs both valid and invalid examples", doc.ID), Fix: "add at least one of each"}}
	}
	return strings.TrimRight(string(raw), "\n"), doc.ID, len(ex.Valid) + len(ex.Invalid), nil
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
func verify(ctx context.Context, bin string, dirs []string, entries []entry) []Fault {
	scratch, err := os.MkdirTemp("", "june-rules-")
	if err != nil {
		return []Fault{{Cause: "the examples cannot be run", Fix: "make the temporary directory writable"}}
	}
	defer func() { _ = os.RemoveAll(scratch) }() // Best-effort scratch cleanup cannot change verification.
	if real, err := filepath.EvalSymlinks(scratch); err == nil {
		scratch = real
	}

	where := make(map[string]Place, len(entries))
	for _, e := range entries {
		where[e.id] = e.place
	}
	if faults := stage(scratch, dirs, entries); len(faults) > 0 {
		return faults
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "test", "--config", filepath.Join(scratch, "sgconfig.yml"), "--skip-snapshot-tests", "--include-off", "--color=never")
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
	for _, c := range misbehaving(text) {
		path := filepath.Join(testsDir, c.rule+"-test.yml")
		faults = append(faults, Fault{
			Dir:   where[c.rule].Dir,
			Path:  path,
			Cause: fmt.Sprintf("rule %s %s example %q %s", c.rule, c.kind, c.code, c.problem),
			Fix:   fmt.Sprintf(exampleFixHint, path),
		})
	}
	if len(faults) > 0 {
		return faults
	}
	for _, m := range failLine.FindAllStringSubmatch(text, -1) {
		path := filepath.Join(testsDir, m[1]+"-test.yml")
		faults = append(faults, Fault{
			Dir:   where[m[1]].Dir,
			Path:  path,
			Cause: fmt.Sprintf("an example of rule %s misbehaves (%s)", m[1], caseDetails(text)),
			Fix:   fmt.Sprintf(exampleFixHint, path),
		})
	}
	if len(faults) > 0 {
		return faults
	}
	if m := parseLine.FindStringSubmatch(text); m != nil {
		at := where[strings.TrimSuffix(strings.TrimPrefix(m[1], "rules"+string(filepath.Separator)), ".yml")]
		return []Fault{{Dir: at.Dir, Path: at.Path, Cause: "ast-grep cannot parse the rule (" + oneLine(failureReason(text)) + ")", Fix: "fix the rule so ast-grep accepts it"}}
	}
	return []Fault{{Cause: "the examples cannot be run", Fix: "run ast-grep test on the rules directory to see why"}}
}

// stagedFile is one file already written into the scratch tree.
type stagedFile struct {
	path  string
	place Place
}

// stage lays rule files and examples out the way ast-grep test expects them.
// A destination that already exists is a duplicate rule id and is left untouched.
func stage(scratch string, dirs []string, entries []entry) []Fault {
	if _, err := writeConfig(scratch); err != nil {
		return []Fault{{Cause: "the examples cannot be run", Fix: "make the rules directory readable"}}
	}
	if err := os.MkdirAll(filepath.Join(scratch, testsDir), 0o755); err != nil {
		return []Fault{{Cause: "the examples cannot be run", Fix: "make the rules directory readable"}}
	}
	var laid []stagedFile
	for _, e := range entries {
		dir := dirs[e.place.Dir]
		ruleDst := filepath.Join(scratch, "rules", e.id+".yml")
		if fault, bad := lay(filepath.Join(dir, e.place.Path), ruleDst, e, &laid); bad {
			return []Fault{fault}
		}
		side := filepath.Join(testsDir, e.id+"-test.yml")
		if fault, bad := lay(filepath.Join(dir, side), filepath.Join(scratch, side), e, &laid); bad {
			return []Fault{fault}
		}
	}
	return nil
}

// lay copies src to a new file at dst. An existing dst is a duplicate of a rule already staged.
func lay(src, dst string, e entry, laid *[]stagedFile) (Fault, bool) {
	if err := copyFile(src, dst); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Fault{Cause: "the examples cannot be run", Fix: "make the rules directory readable"}, true
		}
		f := Fault{
			Dir: e.place.Dir, Path: e.place.Path,
			Cause: fmt.Sprintf("the rule id %s is duplicated", e.id),
			Fix:   "give every rule a unique id",
		}
		if info, statErr := os.Lstat(dst); statErr == nil {
			for _, prev := range *laid {
				pinfo, lerr := os.Lstat(prev.path)
				if lerr == nil && os.SameFile(info, pinfo) {
					also := prev.place
					f.Also = &also
					break
				}
			}
		}
		return f, true
	}
	*laid = append(*laid, stagedFile{path: dst, place: e.place})
	return Fault{}, false
}

// copyFile writes dst as a new file. An existing destination is left untouched.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// misbehavior is one example that ast-grep test reported.
type misbehavior struct {
	rule, kind, code, problem string
}

var caseHeader = regexp.MustCompile(`^\[(Noisy|Missing)\] Expect (?:rule )?(\S+) to `)

func closesExample(line string) bool {
	return strings.HasPrefix(line, "PASS ") || strings.HasPrefix(line, "FAIL ") || strings.HasPrefix(line, "SKIP ") || strings.HasPrefix(line, "Error: ")
}

// misbehaving reads the individual examples out of the case details of ast-grep test output.
func misbehaving(text string) []misbehavior {
	var out []misbehavior
	var cur *misbehavior
	var code []string
	flush := func() {
		if cur != nil {
			cur.code = oneLine(strings.Join(code, "\n"))
			out = append(out, *cur)
		}
		cur, code = nil, nil
	}
	for _, l := range strings.Split(text, "\n") {
		if m := caseHeader.FindStringSubmatch(l); m != nil {
			flush()
			cur = &misbehavior{rule: m[2], kind: "valid", problem: "is reported by the rule"}
			if m[1] == "Missing" {
				cur.kind, cur.problem = "invalid", "is not reported by the rule"
			}
			continue
		}
		if closesExample(l) {
			flush()
			continue
		}
		if cur != nil {
			code = append(code, l)
		}
	}
	flush()
	return out
}

// caseDetails extracts the failing example report from ast-grep test output.
func caseDetails(text string) string {
	const marker = "-- Case Details -----------"
	if _, after, ok := strings.Cut(text, marker); ok {
		text = after
	}
	var code []string
	for _, l := range strings.Split(text, "\n") {
		if closesExample(l) {
			break
		}
		code = append(code, l)
	}
	return oneLine(strings.Join(code, "\n"))
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
