// Package rules owns ast-grep rules and their examples.
package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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

	// isolatedConfig is the only ast-grep configuration june ever uses, so a run depends on
	// the given rules and the touched files and never on an sgconfig.yml found in the working directory.
	isolatedConfig = "ruleDirs: [rules]\nutilDirs: [utils]\ntestConfigs:\n  - testDir: " + testsDir + "\n"

	// utilsDir is the directory of a rules directory that holds the global utility rules its rules share.
	utilsDir = "utils"
)

// Place is a file inside one of the rules directories given to Load.
// Dir indexes that argument list and Path is relative to the directory.
type Place struct {
	Dir  int
	Path string
}

// NoDir is the Fault.Dir of a fault that concerns no rules directory, such as a missing ast-grep.
const NoDir = -1

// Fault is one reason a rule directory cannot be trusted.
// Dir is the index of the directory it concerns in the list given to Load, or NoDir.
// Path is relative to that directory, empty when the fault concerns the directory itself.
// Rule is the id of the rule it concerns, when it concerns one.
// Also is the other file involved when two files conflict, such as a rule id defined twice.
type Fault struct {
	Dir   int
	Path  string
	Rule  string
	Cause string
	Fix   string
	Also  *Place
}

// general is a fault that concerns no rules directory.
func general(cause, fix string) Fault { return Fault{Dir: NoDir, Cause: cause, Fix: fix} }

// entry is one rule file waiting for its examples to be verified.
type entry struct {
	place Place
	id    string
	doc   string
	ex    exampleDoc
	meta  ruleMeta
}

// ruleMeta is what a finding of a rule says beyond its message.
type ruleMeta struct{ note, url string }

// ruleSource is one verified rule, as the text ast-grep reads from a file named after its id.
type ruleSource struct{ id, text string }

// utilEntry is one utility rule file, shared by the rules of every directory.
type utilEntry struct {
	place Place
	ruleSource
}

// RuleSet is a set of rules whose examples all behaved.
type RuleSet struct {
	bin      string
	version  string
	docs     []ruleSource
	utils    []ruleSource
	meta     map[string]ruleMeta
	ids      map[string]bool
	rules    int
	examples int
	// scoped is true when some rule carries files or ignores, which only hold for a file staged at its real path.
	scoped bool
}

// Rules is the number of rules that were verified.
func (rs *RuleSet) Rules() int { return rs.rules }

// Version is the ast-grep version the set was verified with, such as "0.51.3".
func (rs *RuleSet) Version() string { return rs.version }

// Examples is the number of valid and invalid examples that were verified.
func (rs *RuleSet) Examples() int { return rs.examples }

// Match is one rule match anchored at its first line. Line and Col are one-based.
type Match struct {
	File      string
	Line, Col int
	Rule      string
	Severity  string
	Msg       string
	// Note and URL are the rule's note and url fields, empty when it has none.
	Note, URL string
}

// Scanned is what a scan of files found.
type Scanned struct {
	Matches []Match
	OptOuts []OptOut
	// ParseErrors lists the line spans where the parser recovered from a syntax error, sorted by file path and starting line.
	ParseErrors []ParseError
	// Skipped lists the files that could not be read as text and so were not scanned, in the order given.
	Skipped []Skipped
}

// ParseError is a span of lines, one-based and inclusive, that the parser could not read as valid Python.
// Rules match on whatever the parser recovered there, so little may have been checked.
type ParseError struct {
	File     string
	From, To int
}

// parseErrorID names the built-in rule that finds syntax errors. A user rule cannot take it.
const parseErrorID = "june-parse-error"

const parseErrorRule = "id: " + parseErrorID + "\nlanguage: python\nseverity: hint\nmessage: syntax error\nrule:\n  kind: ERROR\n"

// Skipped is a Python file Scan left out. File is the path as given to Scan and Reason says why, without the path.
type Skipped struct {
	File   string
	Reason string
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
	ID       string    `yaml:"id"`
	Message  string    `yaml:"message"`
	Language string    `yaml:"language"`
	Note     yaml.Node `yaml:"note"`
	URL      yaml.Node `yaml:"url"`
}

type exampleDoc struct {
	ID      string   `yaml:"id"`
	Valid   []string `yaml:"valid"`
	Invalid []string `yaml:"invalid"`
}

// ordinal names which example of the given kind after the previous failure has the code ast-grep printed, or zero when it cannot tell.
// Numbers count from one within the valid or invalid list of the examples file.
func (ex exampleDoc) ordinal(kind, code string, after int) int {
	list := ex.Valid
	if kind == "invalid" {
		list = ex.Invalid
	}
	for i, c := range list {
		if i >= after && strings.TrimSuffix(strings.ReplaceAll(c, "\r\n", "\n"), "\n") == code {
			return i + 1
		}
	}
	return 0
}

// Load verifies every rule in every dir against its sidecar examples and merges them into one set.
// The same rule id in two files is a fault, wherever the files live. Ids that differ only by case are the same id.
// Faults non-empty means a nil set, never a partial set.
func Load(ctx context.Context, dirs ...string) (*RuleSet, []Fault) {
	return load(ctx, "", dirs)
}

// LoadRule is Load for the examples of one rule: every rule file is still read, so a fault anywhere still refuses,
// but only the rule with the given id is verified, and the set holds only that rule.
func LoadRule(ctx context.Context, id string, dirs ...string) (*RuleSet, []Fault) {
	return load(ctx, id, dirs)
}

func load(ctx context.Context, only string, dirs []string) (*RuleSet, []Fault) {
	bin, err := exec.LookPath(astGrepName)
	if err != nil {
		return nil, []Fault{general("ast-grep was not found", astGrepFindFix)}
	}
	version, fault := versionFault(ctx, bin)
	if fault != nil {
		if err := ctx.Err(); err != nil {
			f := general("the ast-grep version check was cancelled", "run june again")
			fault = &f
		}
		return nil, []Fault{*fault}
	}
	if len(dirs) == 0 {
		return nil, []Fault{general("no rules directory was given", "point --rules at a rules directory")}
	}

	var faults []Fault
	var entries []entry
	var utils []utilEntry
	places := map[string]Place{}
	utilPlaces := map[string]Place{}
	for i, dir := range dirs {
		info, err := os.Stat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory does not exist", Fix: "create it or point --rules at an existing rules directory"})
			continue
		case err != nil:
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory cannot be read", Fix: "make the rules directory readable"})
			continue
		case !info.IsDir():
			faults = append(faults, Fault{Dir: i, Cause: "the rules path is a file, not a directory", Fix: "point --rules at the directory that holds the rule files, not at one rule file"})
			continue
		}
		files, err := ruleFiles(dir)
		if err != nil {
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory cannot be read", Fix: "make the rules directory readable"})
			continue
		}
		if len(files) == 0 {
			faults = append(faults, Fault{Dir: i, Cause: "the rules directory holds no rules", Fix: "add a rule file such as NAME.yml with its rule-tests/NAME-test.yml, or point --rules elsewhere"})
			continue
		}
		for _, name := range files {
			e, rfs := readRule(dir, name)
			for _, f := range rfs {
				f.Dir = i
				faults = append(faults, f)
			}
			if e.id == "" {
				continue
			}
			if first, dup := knownID(places, e.id); dup {
				faults = append(faults, Fault{Dir: i, Path: name, Rule: e.id, Cause: fmt.Sprintf("the rule id %s is duplicated", e.id), Fix: "give every rule a unique id", Also: &first})
				continue
			}
			here := Place{Dir: i, Path: name}
			if strings.EqualFold(e.id, parseErrorID) {
				faults = append(faults, Fault{Dir: i, Path: name, Rule: e.id, Cause: fmt.Sprintf("the rule id %s is reserved", e.id), Fix: "rename the rule"})
				continue
			}
			places[e.id] = here
			e.place = here
			entries = append(entries, e)
		}
		us, ufs := readUtils(dir)
		for _, f := range ufs {
			f.Dir = i
			faults = append(faults, f)
		}
		for _, u := range us {
			u.place.Dir = i
			if first, dup := knownID(utilPlaces, u.id); dup {
				faults = append(faults, Fault{Dir: i, Path: u.place.Path, Cause: fmt.Sprintf("the utility rule id %s is duplicated", u.id), Fix: "give every utility rule a unique id", Also: &first})
				continue
			}
			utilPlaces[u.id] = u.place
			utils = append(utils, u)
		}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	all := entries
	if only != "" {
		var kept []entry
		for _, e := range entries {
			if e.id == only {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			known := make([]string, len(entries))
			for i, e := range entries {
				known[i] = e.id
			}
			sort.Strings(known)
			return nil, []Fault{general(fmt.Sprintf("no rule has the id %s (rules found: %s)", only, strings.Join(known, ", ")), "pass the id of a rule in the rules directories to --rule")}
		}
		entries = kept
	}
	if faults := verify(ctx, bin, dirs, all, utils, only); len(faults) > 0 {
		return nil, faults
	}
	docs := make([]ruleSource, len(entries))
	ids := make(map[string]bool, len(entries))
	meta := make(map[string]ruleMeta, len(entries))
	total := 0
	scoped := false
	for i, e := range entries {
		docs[i] = ruleSource{id: e.id, text: e.doc}
		ids[e.id] = true
		meta[e.id] = e.meta
		total += len(e.ex.Valid) + len(e.ex.Invalid)
		for _, k := range ruleKeys([]byte(e.doc)) {
			scoped = scoped || k == "files" || k == "ignores"
		}
	}
	us := make([]ruleSource, len(utils))
	for i, u := range utils {
		us[i] = u.ruleSource
	}
	return &RuleSet{bin: bin, version: version, docs: docs, utils: us, meta: meta, ids: ids, rules: len(entries), examples: total, scoped: scoped}, nil
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
// Files are slash-separated paths relative to root. Matches are anchored at their first line.
// File is reported as given. Matches, opt-outs and skipped files follow the order the files were given.
//
// One scratch tree holds the rules and every file at its given relative path, so ast-grep makes one
// comment pass and one scan however many files and rules there are. Nothing travels through argv.
func (rs *RuleSet) Scan(ctx context.Context, root string, files []string) (Scanned, error) {
	var py []string
	for _, f := range files {
		if filepath.Ext(f) == pythonExtension {
			py = append(py, f)
		}
	}
	if len(py) == 0 {
		return Scanned{}, nil
	}
	scratch, err := scratchDir()
	if err != nil {
		return Scanned{}, errScanSetup
	}
	defer func() { _ = os.RemoveAll(scratch) }() // Best-effort scratch cleanup cannot change the scan result.
	if err := rs.writeRules(scratch); err != nil {
		return Scanned{}, errScanSetup
	}

	// Read and stage files before ast-grep starts, so a file I/O failure refuses the whole scan.
	// A file that may hold a comment of interest is also staged in a tree of its own for the comment pass.
	var out Scanned
	var marked []int // indexes into py
	sources := map[int][]byte{}
	staged := make([]string, len(py)) // the path each file has under treeDir
	byStaged := make(map[string]int, len(py))
	for i, f := range py {
		if !filepath.IsLocal(filepath.FromSlash(f)) {
			return Scanned{}, errors.New("a Python file is not inside the scanned tree")
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			return Scanned{}, errors.New("a Python file cannot be read")
		}
		source, reason := toUTF8(source)
		if reason != "" {
			out.Skipped = append(out.Skipped, Skipped{File: f, Reason: reason})
			continue
		}
		name, err := stageSource(filepath.Join(scratch, treeDir), f, i, source)
		if err != nil {
			return Scanned{}, errScanSetup
		}
		if name != f && rs.scoped {
			// A file at a stand-in path would be judged by the filters against the wrong path, silently.
			return Scanned{}, errors.New("a Python file cannot be staged at its own path, so a rule's files and ignores cannot be applied to it")
		}
		staged[i], byStaged[name] = name, i
		if bytes.Contains(source, []byte(optOutMarker)) || bytes.Contains(source, []byte(suppressionMarker)) {
			if err := writeFile(filepath.Join(scratch, commentsDir, filepath.FromSlash(name)), source); err != nil {
				return Scanned{}, errScanSetup
			}
			marked = append(marked, i)
			sources[i] = source
		}
	}

	if len(out.Skipped) == len(py) {
		return out, nil // nothing was staged for ast-grep to scan
	}
	if len(marked) > 0 {
		comments, err := rs.comments(ctx, scratch, byStaged)
		if err != nil {
			return Scanned{}, err
		}
		for _, i := range marked {
			cs := comments[i]
			if err := checkComments(cs, len(sources[i])); err != nil {
				return Scanned{}, err
			}
			out.OptOuts = append(out.OptOuts, rs.optOuts(py[i], sources[i], cs)...)
			disableSuppressions(sources[i], cs)
			// The staged file is replaced by the same text with ast-grep's own suppression comments defused.
			if err := writeFile(filepath.Join(scratch, treeDir, filepath.FromSlash(staged[i])), sources[i]); err != nil {
				return Scanned{}, errScanSetup
			}
		}
	}

	// ast-grep matches a rule's files and ignores against the path under its project root, the directory of
	// the configuration, so the scan runs from inside the tree and sees every file at its real path.
	stdout, err := rs.run(ctx, scratch, treeDir, filepath.Join(scratch, treeDir, "sgconfig.yml"), "scan", "--json=stream", ".")
	if err != nil {
		return Scanned{}, err
	}
	type found struct {
		file int
		Match
	}
	var all []found
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var m struct {
			File     string `json:"file"`
			RuleID   string `json:"ruleId"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Range    struct {
				Start struct{ Line, Column int } `json:"start"`
				End   struct{ Line, Column int } `json:"end"`
			} `json:"range"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Scanned{}, errors.New("ast-grep scan returned unreadable output")
		}
		i, ok := byStaged[strings.TrimPrefix(filepath.ToSlash(m.File), "./")]
		if !ok {
			return Scanned{}, errors.New("ast-grep scan returned unreadable output")
		}
		if m.RuleID == parseErrorID {
			to := m.Range.End.Line + 1
			if m.Range.End.Column == 0 && m.Range.End.Line > m.Range.Start.Line {
				to = m.Range.End.Line // the node ends at the start of the next line
			}
			out.ParseErrors = append(out.ParseErrors, ParseError{File: py[i], From: m.Range.Start.Line + 1, To: to})
			continue
		}
		meta := rs.meta[m.RuleID]
		all = append(all, found{i, Match{
			File: py[i], Line: m.Range.Start.Line + 1, Col: m.Range.Start.Column + 1,
			Rule: m.RuleID, Severity: m.Severity, Msg: m.Message, Note: meta.note, URL: meta.url,
		}})
	}
	// ast-grep scans files on a thread pool, so its stream has no stable order.
	sort.SliceStable(all, func(a, b int) bool {
		x, y := all[a], all[b]
		switch {
		case x.file != y.file:
			return x.file < y.file
		case x.Line != y.Line:
			return x.Line < y.Line
		case x.Col != y.Col:
			return x.Col < y.Col
		}
		return x.Rule < y.Rule
	})
	for _, f := range all {
		out.Matches = append(out.Matches, f.Match)
	}
	sort.SliceStable(out.ParseErrors, func(a, b int) bool {
		x, y := out.ParseErrors[a], out.ParseErrors[b]
		if x.File != y.File {
			return x.File < y.File
		}
		return x.From < y.From
	})
	return out, nil
}

var errScanSetup = errors.New("the scan cannot create its configuration")

const (
	treeDir     = "tree"     // the files to scan, at the paths they have under the scanned root
	commentsDir = "comments" // the files whose comments are read, at the same paths
	altDir      = "_june"    // where a file lands when its own path cannot be staged
)

// scratchDir makes a scratch directory whose path is free of symlinks, so output can be scrubbed of it.
func scratchDir() (string, error) {
	dir, err := os.MkdirTemp("", "june-scan-")
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real, nil
	}
	return dir, nil
}

// writeConfig lays out the isolated ast-grep configuration in dir.
func writeConfig(dir string) error {
	for _, sub := range []string{"rules", utilsDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "sgconfig.yml"), []byte(isolatedConfig), 0o644)
}

// writeTreeConfig puts the configuration of the scan at the root of the staged tree, with the rules beside the tree.
func writeTreeConfig(dir string) error {
	return writeFile(filepath.Join(dir, treeDir, "sgconfig.yml"), []byte("ruleDirs: [../rules]\nutilDirs: [../utils]\n"))
}

// writeRules lays out the configuration in dir and every rule once, in the file named after its id.
func (rs *RuleSet) writeRules(dir string) error {
	if err := writeConfig(dir); err != nil {
		return err
	}
	if err := writeTreeConfig(dir); err != nil {
		return err
	}
	for _, r := range rs.docs {
		if err := os.WriteFile(filepath.Join(dir, "rules", r.id+".yml"), []byte(r.text), 0o644); err != nil {
			return err
		}
	}
	for _, u := range rs.utils {
		if err := os.WriteFile(filepath.Join(dir, utilsDir, u.id+".yml"), []byte(u.text), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "rules", parseErrorID+".yml"), []byte(parseErrorRule), 0o644)
}

// stageSource writes source under tree at its own relative path and returns that path.
// A path ast-grep could not report faithfully (not UTF-8), or one that is already taken
// (a case-insensitive twin, a duplicate), is staged under a name of its own instead.
func stageSource(tree, rel string, n int, source []byte) (string, error) {
	if utf8.ValidString(rel) {
		if err := writeNew(filepath.Join(tree, filepath.FromSlash(rel)), source); err == nil {
			return rel, nil
		}
	}
	alt := fmt.Sprintf("%s/%d.py", altDir, n)
	return alt, writeNew(filepath.Join(tree, filepath.FromSlash(alt)), source)
}

// writeNew creates path and its directories, and fails when path already exists.
func writeNew(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
	}
	return werr
}

// writeFile creates or replaces path and its directories.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// run runs ast-grep in scratch against its configuration, one process for every staged file.
// Exit 1 only says that error-severity matches were found.
// A failure carries the first line ast-grep wrote to standard error, without the scratch path.
// It runs in the scratch subdirectory cwd, against the configuration file config.
func (rs *RuleSet) run(ctx context.Context, scratch, cwd, config string, args ...string) ([]byte, error) {
	args = append(args, "--config", config, "--color=never")
	// The staged tree is not a repository: no ignore file, dot directory or parent directory may hide a file in it.
	for _, kind := range []string{"hidden", "dot", "vcs", "parent", "global", "exclude"} {
		args = append(args, "--no-ignore", kind)
	}
	cmd := exec.CommandContext(ctx, rs.bin, args...)
	cmd.Dir = filepath.Join(scratch, cwd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			msg := "ast-grep could not complete the scan"
			if reason := reasonLine(stderr.String(), scratch); reason != "" {
				msg += ": " + reason
			}
			return nil, errors.New(msg)
		}
	}
	return stdout.Bytes(), nil
}

// reasonLine is the first non-blank line of ast-grep's standard error, with the scratch path removed and a bounded length.
func reasonLine(stderr, scratch string) string {
	stderr = strings.ReplaceAll(stderr, scratch+string(filepath.Separator), "")
	stderr = strings.ReplaceAll(stderr, scratch, ".")
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 200 {
				l = string(r[:200]) + "..."
			}
			return l
		}
	}
	return ""
}

// comment is a Python comment node. Line and Col are one-based, start and end are byte offsets.
type comment struct {
	line, col, start, end int
}

// comments finds the comment nodes of every file in the comments tree, so a marker inside a string literal is never one.
// It returns them by file index, each file's in source order.
func (rs *RuleSet) comments(ctx context.Context, scratch string, byStaged map[string]int) (map[int][]comment, error) {
	stdout, err := rs.run(ctx, scratch, ".", filepath.Join(scratch, "sgconfig.yml"), "run", "--lang", "python", "--kind", "comment", "--json=stream", commentsDir)
	if err != nil {
		return nil, err
	}
	out := map[int][]comment{}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var m struct {
			File  string
			Range struct {
				ByteOffset struct{ Start, End int }
				Start      struct{ Line, Column int }
			}
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, errors.New("ast-grep returned unreadable comment ranges")
		}
		i, ok := byStaged[strings.TrimPrefix(filepath.ToSlash(m.File), commentsDir+"/")]
		if !ok {
			return nil, errors.New("ast-grep returned unreadable comment ranges")
		}
		out[i] = append(out[i], comment{line: m.Range.Start.Line + 1, col: m.Range.Start.Column + 1, start: m.Range.ByteOffset.Start, end: m.Range.ByteOffset.End})
	}
	for _, cs := range out {
		sort.Slice(cs, func(a, b int) bool { return cs[a].start < cs[b].start })
	}
	return out, nil
}

// checkComments refuses comment ranges that do not lie inside a source of the given size.
func checkComments(cs []comment, size int) error {
	for _, c := range cs {
		if c.start < 0 || c.end <= c.start || c.end > size {
			return errors.New("ast-grep returned invalid comment ranges")
		}
	}
	return nil
}

// disableSuppressions defuses ast-grep's own suppression comments in place, so only june opt-outs suppress.
func disableSuppressions(source []byte, cs []comment) {
	for _, c := range cs {
		text := source[c.start:c.end]
		copy(text, bytes.ReplaceAll(text, []byte(suppressionMarker), []byte("ast_grep_ignore")))
	}
}

// optOuts reads from the first `june:` marker's enclosing `#` segment to the end of each comment.
// Markers that do not begin their segment are diagnosed as malformed rather than silently ignored.
func (rs *RuleSet) optOuts(file string, source []byte, cs []comment) []OptOut {
	var out []OptOut
	for _, c := range cs {
		text := string(source[c.start:c.end])
		at := strings.Index(text, optOutMarker)
		if at < 0 {
			continue
		}
		start := strings.LastIndex(text[:at], "#") // Every Python comment starts with #.
		o := OptOut{File: file, Line: c.line, Col: c.col}
		o.Rule, o.Reason, o.Problem = rs.parseOptOut(text[start:])
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

// readRule reads one rule file and its examples. The entry has an empty id when the faults make the rule unusable.
func readRule(dir, name string) (entry, []Fault) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return entry{}, []Fault{{Path: name, Cause: "the rule cannot be read", Fix: "make the rule file readable"}}
	}
	var doc ruleDoc
	if err := decodeDocument(raw, &doc); err != nil {
		return entry{}, []Fault{{Path: name, Cause: "the rule must be one valid YAML document", Fix: "keep exactly one valid YAML document in the rule file"}}
	}
	if doc.ID == "" {
		return entry{}, []Fault{{Path: name, Cause: "the rule has no id", Fix: "add an id such as MY001"}}
	}
	if doc.ID == "." || doc.ID == ".." || strings.ContainsAny(doc.ID, "/\\") {
		return entry{}, []Fault{{Path: name, Cause: "the rule id is not a filename", Fix: "use a rule id without path separators"}}
	}
	if !strings.EqualFold(doc.Language, "python") {
		return entry{}, []Fault{{Path: name, Rule: doc.ID, Cause: "the rule language is not Python", Fix: "set language to python"}}
	}
	if unused := unusedKeys(raw); len(unused) > 0 {
		return entry{}, []Fault{{Path: name, Rule: doc.ID, Cause: fmt.Sprintf("rule %s has %s, which june does not use", doc.ID, quoteKeys(unused)), Fix: "remove it; june reads only " + strings.Join(usedKeys, ", ") + ", so put the correction in the message"}}
	}
	if strings.TrimSpace(doc.Message) == "" {
		return entry{}, []Fault{{Path: name, Rule: doc.ID, Cause: fmt.Sprintf("rule %s has an empty message", doc.ID), Fix: "write the corrective instruction in the message"}}
	}
	var meta ruleMeta
	for _, field := range []struct {
		name string
		node yaml.Node
		dst  *string
	}{{"note", doc.Note, &meta.note}, {"url", doc.URL, &meta.url}} {
		if field.node.Kind == yaml.AliasNode {
			field.node = *field.node.Alias
		}
		if field.node.Kind == 0 || field.node.Tag == "!!null" {
			continue
		}
		if field.node.Tag != "!!str" {
			return entry{}, []Fault{{Path: name, Rule: doc.ID, Cause: fmt.Sprintf("rule %s has a %s that is not a string", doc.ID, field.name), Fix: "write the " + field.name + " as a string or remove it"}}
		}
		*field.dst = strings.TrimSpace(field.node.Value)
	}

	sidecar := filepath.Join(testsDir, doc.ID+"-test.yml")
	rawEx, err := os.ReadFile(filepath.Join(dir, sidecar))
	if err != nil {
		return entry{}, []Fault{{Path: name, Rule: doc.ID, Cause: fmt.Sprintf("rule %s has no examples", doc.ID), Fix: fmt.Sprintf("add %s with valid and invalid examples", sidecar)}}
	}
	var ex exampleDoc
	if err := decodeDocument(rawEx, &ex); err != nil {
		return entry{}, []Fault{{Path: sidecar, Rule: doc.ID, Cause: "the examples must be one valid YAML document", Fix: "keep exactly one valid YAML document in the examples file"}}
	}
	if ex.ID != doc.ID {
		return entry{}, []Fault{{Path: sidecar, Rule: doc.ID, Cause: "the examples id does not match the rule id", Fix: "set the examples id to " + doc.ID}}
	}
	if len(ex.Valid) == 0 || len(ex.Invalid) == 0 {
		return entry{}, []Fault{{Path: sidecar, Rule: doc.ID, Cause: fmt.Sprintf("rule %s needs both valid and invalid examples", doc.ID), Fix: "add at least one of each"}}
	}
	return entry{id: doc.ID, doc: strings.TrimRight(string(raw), "\n"), ex: ex, meta: meta}, nil
}

// readUtils reads the global utility rules of dir: the rule files directly inside its utils directory.
// A directory without one has none. june validates utility ids here; ast-grep validates their language and matcher during verification.
func readUtils(dir string) ([]utilEntry, []Fault) {
	files, err := ruleFiles(filepath.Join(dir, utilsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []Fault{{Path: utilsDir, Cause: "the utils directory cannot be read", Fix: "make the utils directory readable"}}
	}
	var out []utilEntry
	var faults []Fault
	for _, name := range files {
		path := filepath.Join(utilsDir, name)
		raw, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			faults = append(faults, Fault{Path: path, Cause: "the utility rule cannot be read", Fix: "make the utility rule file readable"})
			continue
		}
		var doc ruleDoc
		if err := decodeDocument(raw, &doc); err != nil {
			faults = append(faults, Fault{Path: path, Cause: "the utility rule must be one valid YAML document", Fix: "keep exactly one valid YAML document in the utility rule file"})
			continue
		}
		if doc.ID == "" || doc.ID == "." || doc.ID == ".." || strings.ContainsAny(doc.ID, "/\\") {
			faults = append(faults, Fault{Path: path, Cause: "the utility rule has no id, or its id is not a filename", Fix: "give it an id such as is-print, without path separators"})
			continue
		}
		out = append(out, utilEntry{place: Place{Path: path}, ruleSource: ruleSource{id: doc.ID, text: strings.TrimRight(string(raw), "\n")}})
	}
	return out, faults
}

// usedKeys are the rule keys june reads, or lets ast-grep apply to the match, the message or the path. note and url travel with each finding.
// Any other key would be dropped silently, so it is refused.
var usedKeys = []string{"id", "language", "message", "note", "url", "severity", "rule", "constraints", "utils", "transform", "rewriters", "files", "ignores"}

// ruleKeys lists the top-level keys of a rule document.
func ruleKeys(raw []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	top := doc.Content[0]
	var keys []string
	for i := 0; i+1 < len(top.Content); i += 2 {
		keys = append(keys, top.Content[i].Value)
	}
	return keys
}

// unusedKeys lists the top-level keys of a rule that june does not use, sorted.
func unusedKeys(raw []byte) []string {
	var out []string
	for _, k := range ruleKeys(raw) {
		if !slices.Contains(usedKeys, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func quoteKeys(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = "the key " + strconv.Quote(k)
	}
	return strings.Join(q, " and ")
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

// verify runs the examples of every rule through ast-grep in a scratch directory.
func verify(ctx context.Context, bin string, dirs []string, entries []entry, utils []utilEntry, only string) []Fault {
	scratch, err := os.MkdirTemp("", "june-rules-")
	if err != nil {
		return []Fault{general("the examples cannot be run", "make the temporary directory writable")}
	}
	defer func() { _ = os.RemoveAll(scratch) }() // Best-effort scratch cleanup cannot change verification.
	if real, err := filepath.EvalSymlinks(scratch); err == nil {
		scratch = real
	}

	where := make(map[string]entry, len(entries))
	for _, e := range entries {
		where[e.id] = e
	}
	if faults := stage(scratch, dirs, entries, utils, only); len(faults) > 0 {
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
		return []Fault{general("the examples were cancelled", "run june again")}
	}

	text := strings.ReplaceAll(stdout.String()+"\n"+stderr.String(), scratch+string(filepath.Separator), "")
	var faults []Fault
	ordinals := make(map[[2]string]int)
	for _, c := range misbehaving(text) {
		path := filepath.Join(testsDir, c.rule+"-test.yml")
		key := [2]string{c.rule, c.kind}
		n := where[c.rule].ex.ordinal(c.kind, c.code, ordinals[key])
		ordinals[key] = n
		ordinal := ""
		if n > 0 {
			ordinal = fmt.Sprintf("#%d ", n)
		}
		faults = append(faults, Fault{
			Dir:   where[c.rule].place.Dir,
			Path:  path,
			Rule:  c.rule,
			Cause: fmt.Sprintf("rule %s %s example %s%q %s", c.rule, c.kind, ordinal, oneLine(c.code), c.problem),
			Fix:   fmt.Sprintf(exampleFixHint, path),
		})
	}
	if len(faults) > 0 {
		return faults
	}
	for _, m := range failLine.FindAllStringSubmatch(text, -1) {
		path := filepath.Join(testsDir, m[1]+"-test.yml")
		faults = append(faults, Fault{
			Dir:   where[m[1]].place.Dir,
			Path:  path,
			Rule:  m[1],
			Cause: fmt.Sprintf("an example of rule %s misbehaves (%s)", m[1], caseDetails(text)),
			Fix:   fmt.Sprintf(exampleFixHint, path),
		})
	}
	if len(faults) > 0 {
		return faults
	}
	if strings.Contains(text, "Cannot parse glob pattern") {
		for _, e := range entries {
			args := append(slices.Clone(cmd.Args[1:]), "--filter", "^"+regexp.QuoteMeta(e.id)+"$")
			filtered := exec.CommandContext(ctx, bin, args...)
			filtered.Dir = scratch
			out, _ := filtered.CombinedOutput()
			if ctx.Err() != nil {
				return []Fault{general("the examples were cancelled", "run june again")}
			}
			if text := string(out); strings.Contains(text, "Cannot parse glob pattern") {
				faults = append(faults, Fault{Dir: e.place.Dir, Path: e.place.Path, Rule: e.id, Cause: "a rule has a files or ignores pattern that is not a valid glob (" + oneLine(failureReason(text)) + ")", Fix: "fix the glob in the rule's files or ignores"})
			}
		}
		if len(faults) > 0 {
			return faults
		}
	}
	if m := parseLine.FindStringSubmatch(text); m != nil {
		name := m[1]
		at := where[strings.TrimSuffix(strings.TrimPrefix(name, "rules"+string(filepath.Separator)), ".yml")]
		return []Fault{{Dir: at.place.Dir, Path: at.place.Path, Rule: at.id, Cause: "ast-grep cannot parse the rule (" + oneLine(failureReason(text)) + ")", Fix: "fix the rule so ast-grep accepts it"}}
	}
	cause := "the examples cannot be run"
	if reason := reasonLine(strings.TrimPrefix(strings.TrimSpace(text), "Error: "), scratch); reason != "" {
		cause += " (" + reason + ")" // ast-grep names no file for an error in a utility rule, so its words are all there is
	}
	return []Fault{general(cause, "run ast-grep test on the rules directory to see why")}
}

// stagedFile is one file already written into the scratch tree.
type stagedFile struct {
	path  string
	place Place
}

// stage lays rule files and examples out the way ast-grep test expects them.
// A destination that already exists is a duplicate rule id and is left untouched.
func stage(scratch string, dirs []string, entries []entry, utils []utilEntry, only string) []Fault {
	if err := writeConfig(scratch); err != nil {
		return []Fault{general("the examples cannot be run", "make the rules directory readable")}
	}
	if err := os.MkdirAll(filepath.Join(scratch, testsDir), 0o755); err != nil {
		return []Fault{general("the examples cannot be run", "make the rules directory readable")}
	}
	for _, u := range utils {
		if err := os.WriteFile(filepath.Join(scratch, utilsDir, u.id+".yml"), []byte(u.text), 0o644); err != nil {
			return []Fault{general("the examples cannot be run", "make the rules directory readable")}
		}
	}
	var laid []stagedFile
	for _, e := range entries {
		dir := dirs[e.place.Dir]
		ruleDst := filepath.Join(scratch, "rules", e.id+".yml")
		if fault, bad := lay(filepath.Join(dir, e.place.Path), ruleDst, e, &laid); bad {
			return []Fault{fault}
		}
		if only != "" && e.id != only {
			continue
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
			return general("the examples cannot be run", "make the rules directory readable"), true
		}
		f := Fault{
			Dir: e.place.Dir, Path: e.place.Path, Rule: e.id,
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
	return writeNew(dst, b)
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
			cur.code = strings.TrimSuffix(strings.TrimPrefix(strings.Join(code, "\n"), "\n"), "\n")
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
			code = append(code, strings.TrimPrefix(l, "  "))
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
