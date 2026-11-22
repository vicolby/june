package diag

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"
)

// marshal indents v; strings, ints and slices of them always marshal.
func marshal(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic("diag: report is not marshalable: " + err.Error())
	}
	return append(data, '\n')
}

func (k Kind) String() string {
	switch k {
	case Failure:
		return "failure"
	case OptOut:
		return "opt-out"
	case Legacy:
		return "legacy"
	}
	return "finding"
}

func (s Severity) String() string {
	if s == Warning {
		return "warning"
	}
	return "error"
}

// jsonReport renders every diagnostic, with the summary, as one JSON document. Paths are as the text prints them.
func jsonReport(sorted []Diagnostic, summary string) []byte {
	type entry struct {
		Kind     string `json:"kind"`
		Severity string `json:"severity"`
		File     string `json:"file,omitempty"`
		Line     int    `json:"line,omitempty"`
		Column   int    `json:"column,omitempty"`
		Rule     string `json:"rule,omitempty"`
		Message  string `json:"message"`
	}
	list := []entry{}
	for _, d := range sorted {
		e := entry{Kind: d.Kind.String(), Severity: d.Sev.String(), Message: d.Msg}
		if d.Kind != Failure {
			e.File, e.Line, e.Column, e.Rule = d.File, d.Line, d.Col, d.Code
		}
		list = append(list, e)
	}
	return marshal(struct {
		Version     int     `json:"version"`
		Diagnostics []entry `json:"diagnostics"`
		Summary     string  `json:"summary"`
	}{1, list, summary})
}

// sarifReport renders a SARIF 2.1.0 log for GitHub code scanning. Paths are repository-relative when known.
// Findings are results at their level, opt-outs and legacy matches are notes, and a failure is a tool
// execution notification, since it has no location.
func sarifReport(sorted []Diagnostic) []byte {
	type text struct {
		Text string `json:"text"`
	}
	type region struct {
		StartLine   int `json:"startLine"`
		StartColumn int `json:"startColumn,omitempty"`
	}
	type physical struct {
		ArtifactLocation struct {
			URI string `json:"uri"`
		} `json:"artifactLocation"`
		Region region `json:"region"`
	}
	type location struct {
		PhysicalLocation physical `json:"physicalLocation"`
	}
	type result struct {
		RuleID              string            `json:"ruleId"`
		Level               string            `json:"level"`
		Message             text              `json:"message"`
		Locations           []location        `json:"locations"`
		PartialFingerprints map[string]string `json:"partialFingerprints"`
	}
	type rule struct {
		ID string `json:"id"`
	}
	type notification struct {
		Level   string `json:"level"`
		Message text   `json:"message"`
	}

	prints := fingerprints(sorted)
	results := []result{}
	notes := []notification{}
	rules := []rule{}
	seen := map[string]bool{}
	successful := true
	for i, d := range sorted {
		if d.Kind == Failure {
			notes = append(notes, notification{d.Sev.String(), text{d.Msg}})
			if d.Sev == Error {
				successful = false
			}
			continue
		}
		var p physical
		p.ArtifactLocation.URI = cmp.Or(d.Path, d.File)
		p.Region = region{max(d.Line, 1), d.Col}
		level := "note"
		if d.Kind == Finding {
			level = "warning"
			if d.Sev == Error {
				level = "error"
			}
		}
		if !seen[d.Code] {
			seen[d.Code] = true
			rules = append(rules, rule{d.Code})
		}
		results = append(results, result{
			RuleID: d.Code, Level: level, Message: text{d.Msg},
			Locations:           []location{{p}},
			PartialFingerprints: map[string]string{"june/v1": prints[i]},
		})
	}
	type invocation struct {
		ExecutionSuccessful bool           `json:"executionSuccessful"`
		Notifications       []notification `json:"toolExecutionNotifications,omitempty"`
	}
	type driver struct {
		Name           string `json:"name"`
		InformationURI string `json:"informationUri"`
		Rules          []rule `json:"rules"`
	}
	type run struct {
		Tool struct {
			Driver driver `json:"driver"`
		} `json:"tool"`
		Invocations []invocation `json:"invocations"`
		Results     []result     `json:"results"`
	}
	var r run
	r.Tool.Driver = driver{"june", "https://github.com/vicolby/june", rules}
	r.Invocations = []invocation{{successful, notes}}
	r.Results = results
	return marshal(struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []run  `json:"runs"`
	}{"https://json.schemastore.org/sarif-2.1.0.json", "2.1.0", []run{r}})
}

// maxSummaryRows keeps the job summary far below GitHub's 1 MiB limit for it.
const maxSummaryRows = 100

// StepSummary renders a Markdown job summary: the summary line and a table of findings, opt-outs and failures.
// Legacy matches are information on untouched lines, so they stay out.
func StepSummary(ds []Diagnostic, summary string) []byte {
	sorted := append([]Diagnostic(nil), ds...)
	sortDiagnostics(sorted)
	var b strings.Builder
	b.WriteString("## june\n\n")
	if summary != "" {
		fmt.Fprintf(&b, "%s\n\n", mdText(summary))
	}
	rows := 0
	omitted := 0
	for _, d := range sorted {
		if d.Kind == Legacy {
			continue
		}
		if rows == maxSummaryRows {
			omitted++
			continue
		}
		if rows == 0 {
			b.WriteString("| Severity | Location | Rule | Message |\n| --- | --- | --- | --- |\n")
		}
		rows++
		where, rule, sev := "", "", d.Sev.String()
		if d.Kind != Failure {
			where = fmt.Sprintf("`%s:%d:%d`", strings.ReplaceAll(escapeControls(cmp.Or(d.Path, d.File)), "`", "'"), d.Line, d.Col)
			rule = mdText(d.Code)
		}
		if d.Kind == OptOut {
			sev = "opt-out"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", sev, where, rule, mdText(d.Msg))
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n%d more not shown; see the log.\n", omitted)
	}
	return []byte(b.String())
}

// mdText makes s safe inside a Markdown table cell.
func mdText(s string) string {
	return strings.NewReplacer("|", "\\|", "<", "&lt;", ">", "&gt;", "`", "\\`").Replace(escapeControls(s))
}
