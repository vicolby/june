// Package diag is the one ruff-shaped vocabulary for findings and june's own failures.
package diag

import (
	"bytes"
	"fmt"
	"sort"
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
)

// Format selects the rendering of a diagnostic list.
type Format string

const (
	Text Format = "text"
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
// f is reserved for future formats. The output carries no ANSI.
func Render(ds []Diagnostic, summary string, f Format) (out []byte, files []File) {
	sorted := append([]Diagnostic(nil), ds...)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })

	var b bytes.Buffer
	for _, d := range sorted {
		switch d.Kind {
		case Failure:
			fmt.Fprintf(&b, "june: error: %s\n", d.Msg)
		default:
			fmt.Fprintf(&b, "%s:%d:%d: %s %s\n", d.File, d.Line, d.Col, d.Code, d.Msg)
		}
	}
	if summary != "" {
		fmt.Fprintf(&b, "%s\n", summary)
	}
	return b.Bytes(), nil
}

func less(a, b Diagnostic) bool {
	if a.Kind != b.Kind {
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
