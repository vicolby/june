package diag_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/vicolby/june/internal/diag"
)

func TestRenderSortsFindings(t *testing.T) {
	ds := []diag.Diagnostic{
		{File: "b.py", Line: 2, Col: 1, Code: "PY012", Msg: "m"},
		{File: "a.py", Line: 10, Col: 3, Code: "PY012", Msg: "m"},
		{File: "a.py", Line: 9, Col: 8, Code: "PY013", Msg: "n"},
		{File: "a.py", Line: 9, Col: 2, Code: "PY012", Msg: "m"},
	}
	out, files := diag.Render(ds, "", diag.Text)
	want := "a.py:9:2: PY012 m\n" +
		"a.py:9:8: PY013 n\n" +
		"a.py:10:3: PY012 m\n" +
		"b.py:2:1: PY012 m\n"
	if string(out) != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
}

func TestRenderIsIndependentOfInputOrder(t *testing.T) {
	a := diag.Diagnostic{File: "a.py", Line: 1, Col: 1, Code: "PY012", Msg: "m"}
	b := diag.Diagnostic{File: "a.py", Line: 2, Col: 1, Code: "PY012", Msg: "m"}
	one, _ := diag.Render([]diag.Diagnostic{a, b}, "", diag.Text)
	two, _ := diag.Render([]diag.Diagnostic{b, a}, "", diag.Text)
	if string(one) != string(two) {
		t.Errorf("order changed output:\n%s\nvs\n%s", one, two)
	}
}

func TestRenderFailure(t *testing.T) {
	ds := []diag.Diagnostic{{Kind: diag.Failure, Msg: "cannot do it. Fix: do that"}}
	out, _ := diag.Render(ds, "", diag.Text)
	if got, want := string(out), "june: error: cannot do it. Fix: do that\n"; got != want {
		t.Errorf("out = %q, want %q", got, want)
	}
}

func TestRenderSummaryAndEmpty(t *testing.T) {
	out, _ := diag.Render(nil, "", diag.Text)
	if len(out) != 0 {
		t.Errorf("empty render = %q, want nothing", out)
	}
	out, _ = diag.Render(nil, "checked 1 file", diag.Text)
	if got, want := string(out), "checked 1 file\n"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestRenderHasNoANSI(t *testing.T) {
	ds := []diag.Diagnostic{
		{File: "a.py", Line: 1, Col: 1, Code: "PY012", Sev: diag.Error, Msg: "m"},
		{Kind: diag.Failure, Msg: "x. Fix: y"},
	}
	out, _ := diag.Render(ds, "", "")
	if strings.Contains(string(out), "\x1b") {
		t.Errorf("out contains an escape sequence: %q", out)
	}
}

func TestRenderEscapesControls(t *testing.T) {
	const text = "café \"quoted\" \\path \x00\x07\b\t\n\v\f\r\x1b[2K\x7f\u0085\u009b"
	const escaped = `café "quoted" \path \x00\a\b\t\n\v\f\r\x1b[2K\x7f\u0085\u009b`
	for _, tc := range []struct {
		name    string
		diags   []diag.Diagnostic
		summary string
		want    string
	}{
		{"finding", []diag.Diagnostic{{Kind: diag.Finding, File: text, Line: 1, Col: 2, Code: text, Msg: text}}, "", escaped + ":1:2: " + escaped + " " + escaped + "\n"},
		{"opt-out", []diag.Diagnostic{{Kind: diag.OptOut, File: text, Line: 1, Col: 2, Code: text, Msg: text}}, "", escaped + ":1:2: " + escaped + " " + escaped + "\n"},
		{"legacy", []diag.Diagnostic{{Kind: diag.Legacy, File: text, Line: 1, Col: 2, Code: text, Msg: text}}, "", "legacy: " + escaped + ":1:2: " + escaped + " " + escaped + "\n"},
		{"failure", []diag.Diagnostic{{Kind: diag.Failure, Msg: text}}, "", "june: error: " + escaped + "\n"},
		{"summary", nil, text, escaped + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := diag.Render(tc.diags, tc.summary, diag.Text)
			if string(out) != tc.want {
				t.Errorf("out = %q, want %q", out, tc.want)
			}
			for _, r := range string(out) {
				if unicode.IsControl(r) && r != '\n' {
					t.Errorf("out contains raw control %U: %q", r, out)
				}
			}
		})
	}
}

func TestRenderLegacyIsLabelledAndSortsAfterFindings(t *testing.T) {
	ds := []diag.Diagnostic{
		{Kind: diag.Legacy, File: "a.py", Line: 1, Col: 2, Code: "PY012", Sev: diag.Warning, Msg: "m"},
		{Kind: diag.Finding, File: "z.py", Line: 9, Col: 1, Code: "PY012", Sev: diag.Error, Msg: "m"},
		{Kind: diag.OptOut, File: "z.py", Line: 8, Col: 1, Code: "PY012", Sev: diag.Warning, Msg: "o"},
	}
	out, _ := diag.Render(ds, "", diag.Text)
	want := "z.py:8:1: PY012 o\nz.py:9:1: PY012 m\nlegacy: a.py:1:2: PY012 m\n"
	if string(out) != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
}

func TestGitHubAnnotatesBesideTheSameText(t *testing.T) {
	ds := []diag.Diagnostic{
		{Kind: diag.Finding, File: "a.py", Line: 3, Col: 5, Code: "PY012", Sev: diag.Error, Msg: "bad"},
		{Kind: diag.Finding, File: "a.py", Line: 4, Col: 1, Code: "PY013", Sev: diag.Warning, Msg: "meh"},
		{Kind: diag.OptOut, File: "a.py", Line: 5, Col: 1, Code: "PY012", Sev: diag.Warning, Msg: "opt-out: why"},
		{Kind: diag.Legacy, File: "a.py", Line: 1, Col: 1, Code: "PY012", Sev: diag.Warning, Msg: "old"},
	}
	text, _ := diag.Render(ds, "june: sum", diag.Text)
	out, files := diag.Render(ds, "june: sum", diag.GitHub)
	if !strings.HasSuffix(string(out), string(text)) {
		t.Errorf("GitHub out does not end with the plain text:\n%s\nvs\n%s", out, text)
	}
	want := "::notice file=a.py,line=5,col=1,title=PY012::opt-out: why\n" +
		"::error file=a.py,line=3,col=5,title=PY012::bad\n" +
		"::warning file=a.py,line=4,col=1,title=PY013::meh\n" +
		"::notice file=a.py,line=1,col=1,title=PY012::old\n"
	if got := string(out[:len(out)-len(text)]); got != want {
		t.Errorf("annotations =\n%s\nwant\n%s", got, want)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
}

func TestGitHubFailureAnnotatesWithoutAFile(t *testing.T) {
	ds := []diag.Diagnostic{{Kind: diag.Failure, Msg: "cannot. Fix: do"}}
	out, _ := diag.Render(ds, "", diag.GitHub)
	want := "::error title=june::cannot. Fix: do\njune: error: cannot. Fix: do\n"
	if string(out) != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestGitHubEscapesWorkflowCommandSyntax(t *testing.T) {
	ds := []diag.Diagnostic{{Kind: diag.Finding, File: "a,b:c%d.py", Line: 1, Col: 1, Code: "PY012", Sev: diag.Error, Msg: "100% ::x\r\nline\x1b[0m"}}
	out, _ := diag.Render(ds, "", diag.GitHub)
	got := string(out)
	annotation, _, _ := strings.Cut(got, "\n")
	annotation += "\n"
	want := "::error file=a%2Cb%3Ac%25d.py,line=1,col=1,title=PY012::100%25 ::x\\r\\nline\\x1b[0m\n"
	if annotation != want {
		t.Errorf("annotation = %q, want %q", annotation, want)
	}
	if strings.Count(annotation, "\n") != 1 {
		t.Errorf("annotation spans several lines: %q", annotation)
	}
}

func TestGitHubAnnotatesThePathFromTheRepositoryRoot(t *testing.T) {
	ds := []diag.Diagnostic{{Kind: diag.Finding, File: "app.py", Path: "sub/app.py", Line: 1, Col: 2, Code: "PY012", Sev: diag.Error, Msg: "m"}}
	out, _ := diag.Render(ds, "", diag.GitHub)
	want := "::error file=sub/app.py,line=1,col=2,title=PY012::m\napp.py:1:2: PY012 m\n"
	if string(out) != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func gitlabDiags() []diag.Diagnostic {
	return []diag.Diagnostic{
		{File: "b.py", Line: 4, Col: 2, Code: "PY013", Sev: diag.Warning, Kind: diag.Finding, Msg: "bare except"},
		{File: "a.py", Line: 2, Col: 9, Code: "PY012", Sev: diag.Error, Kind: diag.Finding, Msg: "Mutable default argument."},
		{File: "a.py", Line: 7, Col: 1, Code: "PY012", Sev: diag.Warning, Kind: diag.OptOut, Msg: "opt-out: legacy shim"},
		{File: "a.py", Line: 9, Col: 1, Code: "PY012", Sev: diag.Warning, Kind: diag.Legacy, Msg: "Mutable default argument."},
		{Kind: diag.Failure, Sev: diag.Error, Msg: "unused"},
	}
}

type qualityEntry struct {
	Description string `json:"description"`
	CheckName   string `json:"check_name"`
	Fingerprint string `json:"fingerprint"`
	Severity    string `json:"severity"`
	Location    struct {
		Path  string `json:"path"`
		Lines struct {
			Begin int `json:"begin"`
		} `json:"lines"`
	} `json:"location"`
}

func TestGitLabKeepsPlainTextAndAddsReportBesideIt(t *testing.T) {
	text, _ := diag.Render(gitlabDiags(), "june: summary", diag.Text)
	out, files := diag.Render(gitlabDiags(), "june: summary", diag.GitLab)
	if string(out) != string(text) {
		t.Errorf("GitLab out =\n%s\nwant the plain text\n%s", out, text)
	}
	if len(files) != 1 || files[0].Path != "gl-code-quality-report.json" {
		t.Fatalf("files = %v, want one gl-code-quality-report.json", files)
	}
}

func TestGitLabReportMapsSeveritiesAndSkipsWhatIsNotAFinding(t *testing.T) {
	_, files := diag.Render(gitlabDiags(), "", diag.GitLab)
	var got []qualityEntry
	if err := json.Unmarshal(files[0].Data, &got); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, files[0].Data)
	}
	type row struct {
		path, check, sev string
		line             int
	}
	var rows []row
	for _, e := range got {
		rows = append(rows, row{e.Location.Path, e.CheckName, e.Severity, e.Location.Lines.Begin})
		if e.Description == "" || e.Fingerprint == "" {
			t.Errorf("entry %+v lacks a description or fingerprint", e)
		}
	}
	want := []row{{"a.py", "PY012", "info", 7}, {"a.py", "PY012", "major", 2}, {"b.py", "PY013", "minor", 4}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v, want %v (error major, warning minor, opt-out info; legacy and failures left out)", rows, want)
	}
}

func TestGitLabFingerprintsAreStableAndUnique(t *testing.T) {
	ds := []diag.Diagnostic{
		{File: "a.py", Line: 2, Col: 1, Code: "PY012", Msg: "m"},
		{File: "a.py", Line: 5, Col: 1, Code: "PY012", Msg: "m"},
		{File: "a.py", Line: 6, Col: 1, Code: "PY013", Msg: "m"},
	}
	prints := func(ds []diag.Diagnostic) []string {
		_, files := diag.Render(ds, "", diag.GitLab)
		var got []qualityEntry
		if err := json.Unmarshal(files[0].Data, &got); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range got {
			out = append(out, e.Fingerprint)
		}
		return out
	}
	first := prints(ds)
	seen := map[string]bool{}
	for _, p := range first {
		if seen[p] {
			t.Errorf("fingerprint %s repeats in %v", p, first)
		}
		seen[p] = true
	}
	reversed := []diag.Diagnostic{ds[2], ds[1], ds[0]}
	if !reflect.DeepEqual(first, prints(reversed)) {
		t.Errorf("input order changed fingerprints")
	}
	// The same findings pushed down by unrelated edits above keep their fingerprints.
	shifted := []diag.Diagnostic{ds[0], ds[1], ds[2]}
	for i := range shifted {
		shifted[i].Line += 10
	}
	if !reflect.DeepEqual(first, prints(shifted)) {
		t.Errorf("moving lines changed fingerprints")
	}
}

func TestGitLabEmptyReportIsAnEmptyArray(t *testing.T) {
	_, files := diag.Render(nil, "june: clean", diag.GitLab)
	if len(files) != 1 || string(files[0].Data) != "[]\n" {
		t.Errorf("files = %+v, want an empty JSON array", files)
	}
}

func TestGitLabReportEscapesNothingElseAndIsDeterministic(t *testing.T) {
	_, one := diag.Render(gitlabDiags(), "", diag.GitLab)
	_, two := diag.Render(gitlabDiags(), "", diag.GitLab)
	if string(one[0].Data) != string(two[0].Data) {
		t.Errorf("two renders differ")
	}
	if strings.ContainsRune(string(one[0].Data), '\x1b') {
		t.Errorf("report carries ANSI")
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"text", "json", "sarif", "github", "gitlab"} {
		if f, ok := diag.ParseFormat(s); !ok || string(f) != s {
			t.Errorf("ParseFormat(%q) = %q, %v", s, f, ok)
		}
	}
	for _, s := range []string{"", "SARIF", "xml"} {
		if _, ok := diag.ParseFormat(s); ok {
			t.Errorf("ParseFormat(%q) accepted", s)
		}
	}
}

func TestJSONIsOnlyTheDocument(t *testing.T) {
	out, files := diag.Render(gitlabDiags(), "june: sum", diag.JSON)
	if files != nil {
		t.Errorf("files = %+v, want none", files)
	}
	var doc struct {
		Version     int `json:"version"`
		Diagnostics []struct {
			Kind, Severity, File, Rule, Message string
			Line, Column                        int
		} `json:"diagnostics"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if doc.Version != 1 || doc.Summary != "june: sum" || len(doc.Diagnostics) != 5 {
		t.Fatalf("doc = %+v", doc)
	}
	// Opt-outs and failures lead, then findings in file order, legacy last.
	var kinds []string
	for _, d := range doc.Diagnostics {
		kinds = append(kinds, d.Kind)
	}
	if want := []string{"opt-out", "failure", "finding", "finding", "legacy"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
	if f := doc.Diagnostics[2]; f.File != "a.py" || f.Line != 2 || f.Column != 9 || f.Rule != "PY012" || f.Severity != "error" {
		t.Errorf("finding = %+v", f)
	}
	if f := doc.Diagnostics[1]; f.File != "" || f.Rule != "" {
		t.Errorf("failure carries a location: %+v", f)
	}
}

func TestJSONEmptyIsAnEmptyList(t *testing.T) {
	out, _ := diag.Render(nil, "", diag.JSON)
	if !strings.Contains(string(out), `"diagnostics": []`) {
		t.Errorf("out = %s", out)
	}
}

type sarifLog struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name  string
				Rules []struct{ ID string }
			}
		}
		Invocations []struct {
			ExecutionSuccessful        bool
			ToolExecutionNotifications []struct {
				Level   string
				Message struct{ Text string }
			}
		}
		Results []struct {
			RuleID    string
			Level     string
			Message   struct{ Text string }
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct{ URI string }
					Region           struct{ StartLine, StartColumn int }
				}
			}
			PartialFingerprints map[string]string
		}
	}
}

func TestSARIFReportsLevelsLocationsAndFailures(t *testing.T) {
	ds := gitlabDiags()
	ds[1].Path = "svc/a.py"
	out, files := diag.Render(ds, "june: sum", diag.SARIF)
	if files != nil {
		t.Errorf("files = %+v, want none", files)
	}
	var log sarifLog
	if err := json.Unmarshal(out, &log); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if log.Version != "2.1.0" || !strings.Contains(log.Schema, "sarif-2.1.0") || len(log.Runs) != 1 {
		t.Fatalf("log = %+v", log)
	}
	run := log.Runs[0]
	if run.Tool.Driver.Name != "june" || len(run.Tool.Driver.Rules) != 2 {
		t.Errorf("driver = %+v", run.Tool.Driver)
	}
	var got []string
	for _, r := range run.Results {
		loc := r.Locations[0].PhysicalLocation
		if r.PartialFingerprints["june/v1"] == "" {
			t.Errorf("%s has no fingerprint", r.RuleID)
		}
		got = append(got, r.Level+" "+r.RuleID+" "+loc.ArtifactLocation.URI)
	}
	want := []string{"note PY012 a.py", "error PY012 svc/a.py", "warning PY013 b.py", "note PY012 a.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("results = %v, want %v", got, want)
	}
	if r := run.Results[1]; r.Locations[0].PhysicalLocation.Region.StartLine != 2 || r.Locations[0].PhysicalLocation.Region.StartColumn != 9 {
		t.Errorf("region = %+v", r.Locations[0].PhysicalLocation.Region)
	}
	inv := run.Invocations[0]
	if inv.ExecutionSuccessful || len(inv.ToolExecutionNotifications) != 2 || inv.ToolExecutionNotifications[0].Level != "error" {
		t.Fatalf("invocation = %+v", inv)
	}
	if last := inv.ToolExecutionNotifications[len(inv.ToolExecutionNotifications)-1]; last.Level != "note" || last.Message.Text != "june: sum" {
		t.Errorf("summary notification = %+v", last)
	}
}

func TestSARIFSummaryPreservesExecutionStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ds         []diag.Diagnostic
		successful bool
	}{
		{"clean", nil, true},
		{"finding", []diag.Diagnostic{{File: "a.py", Line: 1, Col: 1, Code: "X", Sev: diag.Error, Kind: diag.Finding, Msg: "bad"}}, true},
		{"warning", []diag.Diagnostic{{Kind: diag.Failure, Sev: diag.Warning, Msg: "nothing judged"}}, true},
		{"failure", []diag.Diagnostic{{Kind: diag.Failure, Msg: "cannot scan"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, summary := range []string{"", "june: coverage note"} {
				out, _ := diag.Render(tc.ds, summary, diag.SARIF)
				var log sarifLog
				if err := json.Unmarshal(out, &log); err != nil {
					t.Fatal(err)
				}
				inv := log.Runs[0].Invocations[0]
				if inv.ExecutionSuccessful != tc.successful {
					t.Errorf("summary %q: invocation = %+v", summary, inv)
				}
				var notes []string
				for _, n := range inv.ToolExecutionNotifications {
					if n.Level == "note" {
						notes = append(notes, n.Message.Text)
					}
				}
				if summary == "" && len(notes) != 0 || summary != "" && (len(notes) != 1 || notes[0] != summary) {
					t.Errorf("summary %q: note notifications = %v", summary, notes)
				}
			}
		})
	}
}

func TestSARIFCleanRunIsSuccessfulWithEmptyResults(t *testing.T) {
	out, _ := diag.Render(nil, "june: clean", diag.SARIF)
	var log sarifLog
	if err := json.Unmarshal(out, &log); err != nil {
		t.Fatal(err)
	}
	run := log.Runs[0]
	if !run.Invocations[0].ExecutionSuccessful || run.Results == nil || len(run.Results) != 0 || len(run.Tool.Driver.Rules) != 0 {
		t.Errorf("run = %+v", run)
	}
	if !strings.Contains(string(out), `"results": []`) || !strings.Contains(string(out), `"rules": []`) {
		t.Errorf("empty lists must be arrays, not null:\n%s", out)
	}
}

func TestSARIFExecutionStatusDistinguishesWarningsAndErrors(t *testing.T) {
	warning := diag.Diagnostic{Kind: diag.Failure, Sev: diag.Warning, Msg: "nothing judged"}
	failure := diag.Diagnostic{Kind: diag.Failure, Sev: diag.Error, Msg: "cannot scan"}
	finding := diag.Diagnostic{Kind: diag.Finding, Sev: diag.Error, File: "app.py", Line: 1, Code: "X", Msg: "violation"}
	for _, tc := range []struct {
		name    string
		ds      []diag.Diagnostic
		success bool
		levels  []string
	}{
		{"warning", []diag.Diagnostic{warning}, true, []string{"warning"}},
		{"error", []diag.Diagnostic{failure}, false, []string{"error"}},
		{"warning-and-error", []diag.Diagnostic{warning, failure}, false, []string{"error", "warning"}},
		{"finding-with-warning", []diag.Diagnostic{finding, warning}, true, []string{"warning"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := diag.Render(tc.ds, "", diag.SARIF)
			var log sarifLog
			if err := json.Unmarshal(out, &log); err != nil {
				t.Fatal(err)
			}
			if len(log.Runs) != 1 || len(log.Runs[0].Invocations) != 1 {
				t.Fatalf("log = %+v", log)
			}
			inv := log.Runs[0].Invocations[0]
			var levels []string
			for _, n := range inv.ToolExecutionNotifications {
				levels = append(levels, n.Level)
			}
			if inv.ExecutionSuccessful != tc.success || !reflect.DeepEqual(levels, tc.levels) {
				t.Errorf("invocation = %+v, want success %v and levels %v", inv, tc.success, tc.levels)
			}
		})
	}
}

func TestSARIFLineIsNeverZero(t *testing.T) {
	out, _ := diag.Render([]diag.Diagnostic{{File: "a.py", Code: "X", Kind: diag.Finding, Msg: "m"}}, "", diag.SARIF)
	if !strings.Contains(string(out), `"startLine": 1`) {
		t.Errorf("out = %s", out)
	}
}

func TestStepSummaryTablesFindingsAndSkipsLegacy(t *testing.T) {
	ds := gitlabDiags()
	ds[0].Msg = "a|b <c> `d`\nx"
	got := string(diag.StepSummary(ds, "june: 1 error"))
	for _, want := range []string{
		"## june\n\njune: 1 error\n\n",
		"| Severity | Location | Rule | Message |\n",
		"| error | `a.py:2:9` | PY012 | Mutable default argument. |\n",
		"| opt-out | `a.py:7:1` | PY012 | opt-out: legacy shim |\n",
		`| warning | ` + "`b.py:4:2`" + ` | PY013 | a\|b &lt;c&gt; \` + "`d\\`" + `\nx |`,
		"| error |  |  | unused |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a.py:9") {
		t.Errorf("legacy match listed:\n%s", got)
	}
}

func TestStepSummaryCleanRunHasNoTable(t *testing.T) {
	got := string(diag.StepSummary(nil, "june: clean"))
	if got != "## june\n\njune: clean\n\n" {
		t.Errorf("got %q", got)
	}
}

func TestStepSummaryCapsRows(t *testing.T) {
	var ds []diag.Diagnostic
	for i := 1; i <= 130; i++ {
		ds = append(ds, diag.Diagnostic{File: "a.py", Line: i, Code: "X", Kind: diag.Finding, Msg: "m"})
	}
	got := string(diag.StepSummary(ds, ""))
	if rows := strings.Count(got, "\n| error |"); rows != 100 || !strings.Contains(got, "30 more not shown") {
		t.Errorf("rows = %d:\n%s", rows, got[len(got)-80:])
	}
}

var withNote = diag.Diagnostic{File: "a.py", Line: 3, Col: 1, Code: "MY001", Sev: diag.Error, Kind: diag.Finding, Msg: "No print.",
	Note: "Use logging.\nIt keeps output.", URL: "https://example.com/MY001"}

func TestRenderTextPrintsNoteAndURLUnderTheFinding(t *testing.T) {
	out, _ := diag.Render([]diag.Diagnostic{withNote, {File: "b.py", Line: 1, Col: 1, Code: "MY002", Msg: "plain"}}, "", diag.Text)
	want := "a.py:3:1: MY001 No print.\n  note: Use logging.\n        It keeps output.\n  url: https://example.com/MY001\nb.py:1:1: MY002 plain\n"
	if string(out) != want {
		t.Errorf("out =\n%s\nwant\n%s", out, want)
	}
}

func TestRenderTextEscapesControlsInNoteAndURL(t *testing.T) {
	d := withNote
	d.Note, d.URL = "bell\a", "https://example.com/\x1b[31m"
	out, _ := diag.Render([]diag.Diagnostic{d}, "", diag.Text)
	if strings.ContainsAny(string(out), "\a\x1b") {
		t.Errorf("out has raw control characters: %q", out)
	}
}

// Legacy matches and opt-outs are information, so they stay as short as before.
func TestRenderLegacyAndOptOutCarryNoNote(t *testing.T) {
	legacy, optOut := withNote, withNote
	legacy.Kind, optOut.Kind = diag.Legacy, diag.OptOut
	out, _ := diag.Render([]diag.Diagnostic{legacy, optOut}, "", diag.Text)
	if strings.Contains(string(out), "note:") || strings.Contains(string(out), "url:") {
		t.Errorf("out = %q, want no note or url", out)
	}
}

func TestRenderJSONCarriesNoteURLAndAFailuresFileAndRule(t *testing.T) {
	failure := diag.Diagnostic{Kind: diag.Failure, Sev: diag.Error, File: "rules/rule-tests/X-test.yml", Code: "X", Msg: "bad example"}
	out, _ := diag.Render([]diag.Diagnostic{withNote, failure}, "", diag.JSON)
	var doc struct {
		Diagnostics []map[string]any
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.Diagnostics) != 2 {
		t.Fatalf("out = %s, %v", out, err)
	}
	f, finding := doc.Diagnostics[0], doc.Diagnostics[1]
	if f["file"] != "rules/rule-tests/X-test.yml" || f["rule"] != "X" || f["note"] != nil {
		t.Errorf("failure = %v", f)
	}
	if finding["note"] != withNote.Note || finding["url"] != withNote.URL {
		t.Errorf("finding = %v", finding)
	}
	plain, _ := diag.Render([]diag.Diagnostic{{File: "a.py", Line: 1, Col: 1, Code: "R", Msg: "m"}}, "", diag.JSON)
	if strings.Contains(string(plain), `"note"`) || strings.Contains(string(plain), `"url"`) {
		t.Errorf("a finding without a note prints note or url keys: %s", plain)
	}
}

func TestRenderSARIFPutsNoteAndURLOnTheRuleEvenWhenAnOptOutNamesItFirst(t *testing.T) {
	optOut := diag.Diagnostic{File: "a.py", Line: 1, Col: 1, Code: "MY001", Kind: diag.OptOut, Sev: diag.Warning, Msg: "opt-out: reason"}
	out, _ := diag.Render([]diag.Diagnostic{optOut, withNote}, "", diag.SARIF)
	var log struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ID      string
						HelpURI string `json:"helpUri"`
						Help    struct{ Text string }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(out, &log); err != nil {
		t.Fatal(err)
	}
	rules := log.Runs[0].Tool.Driver.Rules
	if len(rules) != 1 || rules[0].ID != "MY001" || rules[0].HelpURI != withNote.URL || rules[0].Help.Text != withNote.Note {
		t.Errorf("rules = %+v, want one MY001 with the url and note", rules)
	}
}

func TestRenderGitHubKeepsNoteInTheAnnotation(t *testing.T) {
	out, _ := diag.Render([]diag.Diagnostic{withNote}, "", diag.GitHub)
	want := "::error file=a.py,line=3,col=1,title=MY001::No print.%0Anote: Use logging.%0AIt keeps output.%0Aurl: https://example.com/MY001\n"
	if !strings.HasPrefix(string(out), want) {
		t.Errorf("out = %q, want prefix %q", out, want)
	}
}

func TestRenderGitLabReportHasContentOnlyWithNoteOrURL(t *testing.T) {
	_, files := diag.Render([]diag.Diagnostic{withNote, {File: "b.py", Line: 1, Col: 1, Code: "R", Sev: diag.Error, Msg: "m"}}, "", diag.GitLab)
	var report []struct {
		CheckName string `json:"check_name"`
		Content   *struct{ Body string }
	}
	if err := json.Unmarshal(files[0].Data, &report); err != nil || len(report) != 2 {
		t.Fatalf("report = %s, %v", files[0].Data, err)
	}
	if report[0].Content == nil || report[0].Content.Body != "Use logging.\nIt keeps output.\n\nhttps://example.com/MY001" || report[1].Content != nil {
		t.Errorf("report = %+v", report)
	}
}

func TestStepSummaryShowsNoteAndURL(t *testing.T) {
	out := string(diag.StepSummary([]diag.Diagnostic{withNote}, ""))
	if !strings.Contains(out, "No print.<br>Use logging.<br>It keeps output.<br>https://example.com/MY001 |") {
		t.Errorf("summary = %q", out)
	}
}
