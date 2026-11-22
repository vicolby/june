// Package diag is the one ruff-shaped vocabulary for findings and june's own failures.
package diag

import (
	"bytes"
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
	// Failure is a june outcome that is not a finding, rendered as june: error: <Msg>.
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
	// GitLab renders the plain text and a GitLab Code Quality report beside it.
	GitLab Format = "gitlab"
)

// GitLabReport is the path of the Code Quality report, which GitLab reads as a codequality artifact.
const GitLabReport = "gl-code-quality-report.json"

type Diagnostic struct {
	File      string
	Line, Col int
	Code      string
	Sev       Severity
	Kind      Kind
	Msg       string
}

// File is an extra artifact rendered beside the plain output.
type File struct {
	Path string
	Data []byte
}

// Render returns sorted plain-text diagnostics, followed by summary when it is not empty.
// The text is identical in every format; GitLab adds its Code Quality report as a File. The output carries no ANSI.
func Render(ds []Diagnostic, summary string, f Format) (out []byte, files []File) {
	sorted := append([]Diagnostic(nil), ds...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })

	var b bytes.Buffer
	for _, d := range sorted {
		switch d.Kind {
		case Failure:
			fmt.Fprintf(&b, "june: error: %s\n", escapeControls(d.Msg))
		case Legacy:
			fmt.Fprintf(&b, "legacy: %s:%d:%d: %s %s\n", escapeControls(d.File), d.Line, d.Col, escapeControls(d.Code), escapeControls(d.Msg))
		default:
			fmt.Fprintf(&b, "%s:%d:%d: %s %s\n", escapeControls(d.File), d.Line, d.Col, escapeControls(d.Code), escapeControls(d.Msg))
		}
	}
	if summary != "" {
		fmt.Fprintf(&b, "%s\n", escapeControls(summary))
	}
	switch f {
	case GitLab:
		return b.Bytes(), []File{{Path: GitLabReport, Data: codeQuality(sorted)}}
	}
	return b.Bytes(), nil
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
	type entry struct {
		Description string   `json:"description"`
		CheckName   string   `json:"check_name"`
		Fingerprint string   `json:"fingerprint"`
		Severity    string   `json:"severity"`
		Location    location `json:"location"`
	}
	type key struct{ file, code, msg string }
	seen := map[key]int{}
	report := []entry{}
	for _, d := range sorted {
		if d.Kind != Finding && d.Kind != OptOut {
			continue
		}
		k := key{d.File, d.Code, d.Msg}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", d.File, d.Code, d.Msg, seen[k])))
		seen[k]++
		report = append(report, entry{
			Description: d.Msg,
			CheckName:   d.Code,
			Fingerprint: hex.EncodeToString(sum[:]),
			Severity:    codeQualitySeverity(d),
			Location:    location{Path: d.File, Lines: lines{Begin: d.Line}},
		})
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		panic("diag: code quality report is not marshalable: " + err.Error()) // strings and ints always marshal
	}
	return append(data, '\n')
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
