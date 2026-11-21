// Package diag is the one ruff-shaped vocabulary for findings and june's own failures.
package diag

import (
	"bytes"
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
	// GitHub is the plain text followed by one GitHub Actions workflow command per diagnostic.
	GitHub Format = "github"
)

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
// The text is the same for every format; a format adds its native report before it, and the
// closed switch below is the only place a format is chosen. The output carries no ANSI.
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
	case GitHub:
		// Commands lead so the text still ends with the summary line, as it does everywhere.
		var a bytes.Buffer
		for _, d := range sorted {
			a.WriteString(annotation(d))
		}
		return append(a.Bytes(), b.Bytes()...), nil
	default: // Text, and "" before a caller has chosen one.
		return b.Bytes(), nil
	}
}

// annotation is a GitHub Actions workflow command for d, ending in a newline.
// A failure has no location, so it is annotated without a file.
// Legacy matches and opt-outs are information, so they are notices.
func annotation(d Diagnostic) string {
	level := "notice"
	switch {
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
	return fmt.Sprintf("::%s file=%s,line=%d,col=%d,title=%s::%s\n", level,
		commandProperty(d.File), d.Line, d.Col, commandProperty(d.Code), commandData(d.Msg))
}

// commandData escapes a workflow command message after control characters are made visible.
func commandData(s string) string {
	return strings.ReplaceAll(escapeControls(s), "%", "%25")
}

// commandProperty also escapes the characters that separate properties.
func commandProperty(s string) string {
	return strings.NewReplacer(":", "%3A", ",", "%2C").Replace(commandData(s))
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
