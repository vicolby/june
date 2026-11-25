// Package diag is the one ruff-shaped vocabulary for findings and june's own failures.
package diag

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Severity ranks a diagnostic. Only an Error fails a check.
type Severity int

const (
	Error Severity = iota
	Warning
)

// Kind says what a diagnostic reports.
type Kind int

const (
	// Finding is a rule match on a touched line, rendered as file:line:col: CODE message.
	Finding Kind = iota
	// Failure is a june outcome that is not a finding, rendered as june: error: <Msg>, or june: warning: <Msg> at Warning severity.
	Failure
	// OptOut is an inline opt-out comment on a touched line, honoured or not, rendered like a finding.
	OptOut
	// Legacy is a rule match on an untouched line, listed as information only, rendered as legacy: file:line:col: CODE message.
	Legacy
)

// Format selects the rendering of a diagnostic list.
type Format string

const (
	Text Format = "text"
	// GitHub is one GitHub Actions workflow command per diagnostic, followed by the plain text.
	GitHub Format = "github"
	// GitLab renders the plain text and a GitLab Code Quality report beside it.
	GitLab Format = "gitlab"
	// JSON is one JSON document of the diagnostics and the summary, and nothing else.
	JSON Format = "json"
	// SARIF is one SARIF 2.1.0 log, which GitHub code scanning accepts, and nothing else.
	SARIF Format = "sarif"
)

// ParseFormat reads a --format value.
func ParseFormat(s string) (Format, bool) {
	switch f := Format(s); f {
	case Text, GitHub, GitLab, JSON, SARIF:
		return f, true
	}
	return "", false
}

// GitLabReport is the path of the Code Quality report, which GitLab reads as a codequality artifact.
const GitLabReport = "gl-code-quality-report.json"

// ReviewGitLabReport is the Code Quality report of june review, a file of its own so that running both commands in one job never overwrites the first.
const ReviewGitLabReport = "gl-code-quality-review-report.json"

// Names says what one command calls its outputs.
type Names struct {
	// Heading is the title of the GitHub job summary section.
	Heading string
	// GitLabReport is the path of the GitLab Code Quality report.
	GitLabReport string
}

// checkNames are the names of the june check, which never change.
var checkNames = Names{Heading: "june", GitLabReport: GitLabReport}

type Diagnostic struct {
	File string
	// File is the path the plain text prints. A Failure may carry the file it concerns, and its Code the rule, for JSON.
	// Path is File relative to the repository root, for formats whose consumer reads it that way.
	// Empty means File already is. Plain text always prints File.
	Path      string
	Line, Col int
	Code      string
	Sev       Severity
	Kind      Kind
	Msg       string
	// Note and URL are the rule's own note and url fields, for a finding. Empty when the rule has none.
	Note, URL string
	// Volatile marks a Msg that differs from run to run for the same problem, such as a model's wording.
	// A GitLab fingerprint then hashes the file, code and line, and leaves the message out.
	Volatile bool
}

// File is an extra artifact rendered beside the plain output.
type File struct {
	// Path is relative to the working directory, or absolute for a file the CI names.
	Path string
	Data []byte
	// Append adds Data to the file instead of replacing it.
	Append bool
}

// Render returns sorted diagnostics in f and carries no ANSI.
// Text is plain text followed by summary when it is not empty. GitHub prepends one workflow command per diagnostic to that text.
// GitLab returns that text and a Code Quality file. JSON and SARIF return one document and no text.
// The closed switch below is the only place a format is chosen.
func Render(ds []Diagnostic, summary string, f Format) (out []byte, files []File) {
	return RenderAs(ds, summary, f, checkNames)
}

// RenderAs is Render for a command with names of its own for the files it writes.
func RenderAs(ds []Diagnostic, summary string, f Format, names Names) (out []byte, files []File) {
	sorted := append([]Diagnostic(nil), ds...)
	sortDiagnostics(sorted)

	var b bytes.Buffer
	for _, d := range sorted {
		switch d.Kind {
		case Failure:
			fmt.Fprintf(&b, "june: %s: %s\n", d.Sev, escapeControls(d.Msg))
		case Legacy:
			fmt.Fprintf(&b, "legacy: %s:%d:%d: %s %s\n", escapeControls(d.File), d.Line, d.Col, escapeControls(d.Code), escapeControls(d.Msg))
		default:
			fmt.Fprintf(&b, "%s:%d:%d: %s %s\n", escapeControls(d.File), d.Line, d.Col, escapeControls(d.Code), escapeControls(d.Msg))
			if d.Kind == Finding {
				b.WriteString(indentedDetails(d))
			}
		}
	}
	if summary != "" {
		fmt.Fprintf(&b, "%s\n", escapeControls(summary))
	}
	switch f {
	case JSON:
		return jsonReport(sorted, summary), nil
	case SARIF:
		return sarifReport(sorted, summary), nil
	case GitHub:
		// Commands lead so the text still ends with the summary line, as the plain text does.
		var a bytes.Buffer
		for _, d := range sorted {
			a.WriteString(annotation(d))
		}
		return append(a.Bytes(), b.Bytes()...), nil
	case GitLab:
		return b.Bytes(), []File{{Path: names.GitLabReport, Data: codeQuality(sorted)}}
	default: // Text, and "" before a caller has chosen one.
		return b.Bytes(), nil
	}
}

// indentedDetails is the rule's note and url under a finding, one indented line each, so the finding line itself stays machine-readable.
// A multi-line note continues on indented lines.
func indentedDetails(d Diagnostic) string {
	var b strings.Builder
	if d.Note != "" {
		lines := strings.Split(strings.TrimSpace(d.Note), "\n")
		for i, l := range lines {
			prefix := "        "
			if i == 0 {
				prefix = "  note: "
			}
			fmt.Fprintf(&b, "%s%s\n", prefix, escapeControls(strings.TrimRight(l, " \t\r")))
		}
	}
	if d.URL != "" {
		fmt.Fprintf(&b, "  url: %s\n", escapeControls(d.URL))
	}
	return b.String()
}

// annotation is a GitHub Actions workflow command for d, ending in a newline.
// A failure has no location, so it is annotated without a file.
// Legacy matches and opt-outs are information, so they are notices.
func annotation(d Diagnostic) string {
	level := "notice"
	switch {
	case d.Kind == Failure && d.Sev == Warning:
		level = "warning"
	case d.Kind == Failure:
		level = "error"
	case d.Kind == Finding && d.Sev == Error:
		level = "error"
	case d.Kind == Finding:
		level = "warning"
	}
	if d.Kind == Failure {
		return fmt.Sprintf("::%s title=june::%s\n", level, commandData(d.Msg))
	}
	msg := commandData(d.Msg)
	if d.Note != "" {
		lines := strings.Split(strings.TrimSpace(d.Note), "\n")
		for i, line := range lines {
			lines[i] = commandData(line)
		}
		msg += "%0Anote: " + strings.Join(lines, "%0A")
	}
	if d.URL != "" {
		msg += "%0Aurl: " + commandData(d.URL)
	}
	return fmt.Sprintf("::%s file=%s,line=%d,col=%d,title=%s::%s\n", level,
		commandProperty(cmp.Or(d.Path, d.File)), d.Line, d.Col, commandProperty(d.Code), msg)
}

// commandData escapes a workflow command message after control characters are made visible.
func commandData(s string) string {
	return strings.ReplaceAll(escapeControls(s), "%", "%25")
}

// commandProperty also escapes the characters that separate properties.
func commandProperty(s string) string {
	return strings.NewReplacer(":", "%3A", ",", "%2C").Replace(commandData(s))
}

// codeQuality renders findings and opt-outs as a GitLab Code Quality report.
// Legacy matches and failures are information or have no location, so they stay out.
// A fingerprint hashes the file, rule, message and the ordinal among identical ones in sorted order,
// so it survives edits that only move a finding to another line.
func codeQuality(sorted []Diagnostic) []byte {
	type lines struct {
		Begin int `json:"begin"`
	}
	type location struct {
		Path  string `json:"path"`
		Lines lines  `json:"lines"`
	}
	type content struct {
		Body string `json:"body"`
	}
	type entry struct {
		Description string   `json:"description"`
		CheckName   string   `json:"check_name"`
		Fingerprint string   `json:"fingerprint"`
		Severity    string   `json:"severity"`
		Location    location `json:"location"`
		Content     *content `json:"content,omitempty"`
	}
	prints := fingerprints(sorted)
	report := []entry{}
	for i, d := range sorted {
		if d.Kind != Finding && d.Kind != OptOut {
			continue
		}
		e := entry{
			Description: d.Msg,
			CheckName:   d.Code,
			Fingerprint: prints[i],
			Severity:    codeQualitySeverity(d),
			Location:    location{Path: d.File, Lines: lines{Begin: d.Line}},
		}
		if body := strings.TrimSpace(strings.TrimSpace(d.Note) + "\n\n" + d.URL); body != "" {
			e.Content = &content{Body: body}
		}
		report = append(report, e)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		panic("diag: code quality report is not marshalable: " + err.Error()) // strings and ints always marshal
	}
	return append(data, '\n')
}

// fingerprints hashes each diagnostic's file, rule, message and ordinal among identical ones in sorted order.
// A volatile diagnostic hashes its line instead of its message.
func fingerprints(sorted []Diagnostic) []string {
	type key struct {
		file, code, msg string
		line            int
	}
	seen := map[key]int{}
	out := make([]string, len(sorted))
	for i, d := range sorted {
		k := key{file: d.File, code: d.Code, msg: d.Msg}
		text := fmt.Sprintf("%s\x00%s\x00%s\x00%d", d.File, d.Code, d.Msg, seen[k])
		if d.Volatile {
			// The wording is not stable, so the place is the identity.
			k = key{file: d.File, code: d.Code, line: d.Line}
			text = fmt.Sprintf("%s\x00%s\x00line %d\x00%d", d.File, d.Code, d.Line, seen[k])
		}
		sum := sha256.Sum256([]byte(text))
		seen[k]++
		out[i] = hex.EncodeToString(sum[:])
	}
	return out
}

// codeQualitySeverity maps a diagnostic to a Code Quality severity: an error is major, a warning minor, an opt-out info.
func codeQualitySeverity(d Diagnostic) string {
	switch {
	case d.Kind == OptOut:
		return "info"
	case d.Sev == Error:
		return "major"
	}
	return "minor"
}

func escapeControls(text string) string {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) {
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool { return less(ds[i], ds[j]) })
}

func less(a, b Diagnostic) bool {
	if a.Kind != b.Kind {
		// Legacy information always closes the list; otherwise opt-outs and failures lead findings.
		if a.Kind == Legacy || b.Kind == Legacy {
			return b.Kind == Legacy
		}
		return a.Kind > b.Kind
	}
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	if a.Col != b.Col {
		return a.Col < b.Col
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	return a.Msg < b.Msg
}
