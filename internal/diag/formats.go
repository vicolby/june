package diag

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
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
		Note     string `json:"note,omitempty"`
		URL      string `json:"url,omitempty"`
	}
	list := []entry{}
	for _, d := range sorted {
		e := entry{Kind: d.Kind.String(), Severity: d.Sev.String(), Message: d.Msg, Note: d.Note, URL: d.URL}
		if d.Kind != Failure {
			e.File, e.Line, e.Column, e.Rule = d.File, d.Line, d.Col, d.Code
		} else {
			e.File, e.Rule = d.File, d.Code // a failure names its file and rule only when it concerns them, as a failing example does
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
func sarifReport(sorted []Diagnostic, summary string) []byte {
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
		ID      string `json:"id"`
		HelpURI string `json:"helpUri,omitempty"`
		Help    *text  `json:"help,omitempty"`
	}
	type notification struct {
		Level   string `json:"level"`
		Message text   `json:"message"`
	}

	prints := fingerprints(sorted)
	results := []result{}
	notes := []notification{}
	rules := []rule{}
	seen := map[string]int{} // rule id to its index in rules
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
		at, known := seen[d.Code]
		if !known {
			at = len(rules)
			seen[d.Code] = at
			rules = append(rules, rule{ID: d.Code})
		}
		// An opt-out can name a rule before its first finding does, so the details are taken from whichever carries them.
		help := d.Note
		if d.URL != "" {
			if absoluteURI(d.URL) {
				if rules[at].HelpURI == "" {
					rules[at].HelpURI = d.URL
				}
			} else {
				if help != "" {
					help += "\n\n"
				}
				help += d.URL
			}
		}
		if rules[at].Help == nil && help != "" {
			rules[at].Help = &text{help}
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
	if summary != "" {
		notes = append(notes, notification{"note", text{summary}})
	}
	r.Invocations = []invocation{{successful, notes}}
	r.Results = results
	return marshal(struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []run  `json:"runs"`
	}{"https://json.schemastore.org/sarif-2.1.0.json", "2.1.0", []run{r}})
}

var uriComponent = regexp.MustCompile(`^(?:[A-Za-z0-9._~!$&'()*+,;=:@/?-]|%[0-9A-Fa-f]{2})*$`)

func absoluteURI(s string) bool {
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() || u.String() != u.Scheme+s[len(u.Scheme):] {
		return false
	}
	if !uriComponent.MatchString(u.Opaque) || !uriComponent.MatchString(u.RawQuery) {
		return false
	}
	if strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(u.Hostname())
		return err == nil && addr.Is6() && addr.Zone() == ""
	}
	return strings.Count(u.Host, ":") <= 1
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
		msg := mdText(d.Msg)
		if d.Note != "" {
			lines := strings.Split(strings.TrimSpace(d.Note), "\n")
			for i, line := range lines {
				lines[i] = mdText(line)
			}
			msg += "<br>" + strings.Join(lines, "<br>")
		}
		if d.URL != "" {
			msg += "<br>" + mdText(d.URL)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", sev, where, rule, msg)
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
