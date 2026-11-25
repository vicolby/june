package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/vicolby/june/internal/diag"
)

const (
	maxFindingsPerCall = 50
	maxReasonRunes     = 300
	namedFiles         = 5
)

type call struct {
	windows []window
	bytes   int
}

type tally struct {
	reviewed, calls, in, out, discarded int
}

func (l Limits) fileBytes() int { return min(l.FileBytes, l.CallBytes) }

// windows renders the files in path order. A file that has touched lines, none of them inside the file, is named in outside.
// A file with no touched lines is not reviewed and not left out. A window over the limit of one file is named in tooLarge.
func (p *Plan) windows(files []Source, outside, tooLarge *[]string) []window {
	sorted := append([]Source(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var out []window
	for _, s := range sorted {
		w, ok := newWindow(s)
		if !ok {
			if len(s.Touched) > 0 {
				*outside = append(*outside, s.Path)
			}
			continue
		}
		if len(w.text) > p.limits.fileBytes() {
			*tooLarge = append(*tooLarge, w.path)
			continue
		}
		out = append(out, w)
	}
	return out
}

// batch fills calls in path order. The first file that no longer fits the run's budget ends it, so a file is only ever
// left out together with everything after it, and over holds those files.
func (p *Plan) batch(windows []window) (batches []call, over []window) {
	total := 0
	for i, w := range windows {
		size := len(w.text)
		if total+size > p.limits.TotalBytes {
			return batches, windows[i:]
		}
		n := len(batches)
		if n == 0 || batches[n-1].bytes+size > p.limits.CallBytes {
			if n == p.limits.MaxCalls {
				return batches, windows[i:]
			}
			batches = append(batches, call{})
			n++
		}
		batches[n-1].windows = append(batches[n-1].windows, w)
		batches[n-1].bytes += size
		total += size
	}
	return batches, nil
}

func paths(batches []call) []string {
	var out []string
	for _, b := range batches {
		for _, w := range b.windows {
			out = append(out, w.path)
		}
	}
	return out
}

// ask makes one call and turns its answer into findings, dropping any that break the guard.
func (p *Plan) ask(ctx context.Context, m Model, b call, t *tally) ([]diag.Diagnostic, error) {
	var in strings.Builder
	in.WriteString("RULES\n" + strings.TrimRight(p.rules, "\r\n") + "\nEND RULES\n")
	byPath := map[string]window{}
	for _, w := range b.windows {
		in.WriteString("\n" + w.text)
		byPath[w.path] = w
	}
	t.calls++
	ans, err := m.Complete(ctx, Query{
		Model:           p.model,
		Effort:          p.effort,
		Instructions:    p.instructions,
		Input:           in.String(),
		Schema:          schema(),
		MaxOutputTokens: p.limits.OutputTokens,
	})
	if err != nil {
		return nil, err
	}
	t.in += ans.InputTokens
	t.out += ans.OutputTokens

	var reply struct {
		Findings *[]struct {
			Path   string `json:"path"`
			Line   int    `json:"line"`
			Reason string `json:"reason"`
		} `json:"findings"`
	}
	if json.Unmarshal([]byte(ans.Text), &reply) != nil || reply.Findings == nil {
		return nil, &CallError{Kind: Malformed}
	}
	type site struct {
		path string
		line int
	}
	seen := map[site]bool{}
	var out []diag.Diagnostic
	for _, f := range *reply.Findings {
		w, ok := byPath[f.Path]
		s := site{f.Path, f.Line}
		if !ok || !w.has(f.Line) || seen[s] || len(out) == maxFindingsPerCall {
			t.discarded++
			continue
		}
		seen[s] = true
		msg := oneLine(f.Reason, maxReasonRunes)
		if msg == "" {
			msg = "possible violation of the rules"
		}
		out = append(out, diag.Diagnostic{File: f.Path, Line: f.Line, Col: 1, Code: Code, Sev: diag.Warning, Kind: diag.Finding, Msg: msg, Volatile: true})
	}
	return out, nil
}

// schema is the JSON Schema of an answer. Strict mode wants every property required and no others,
// so the limits on text are enforced in code instead.
func schema() json.RawMessage {
	type obj = map[string]any
	s := obj{
		"type": "object", "additionalProperties": false, "required": []string{"findings"},
		"properties": obj{"findings": obj{
			"type": "array",
			"items": obj{
				"type": "object", "additionalProperties": false, "required": []string{"path", "line", "reason"},
				"properties": obj{
					"path":   obj{"type": "string"},
					"line":   obj{"type": "integer"},
					"reason": obj{"type": "string"},
				},
			},
		}},
	}
	data, err := json.Marshal(s)
	if err != nil {
		panic("review: schema is not marshalable: " + err.Error()) // strings and slices always marshal
	}
	return data
}

// asCallError is err as a CallError. A deadline is a timeout whatever the model said about it.
func asCallError(ctx context.Context, err error) *CallError {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &CallError{Kind: Timeout}
	}
	return &CallError{Kind: Unavailable}
}

// failure is the warning for a call that failed, naming the files it left unreviewed.
func failure(e *CallError, files []string) diag.Diagnostic {
	var what, fix string
	status := ""
	if e.Status != 0 {
		status = fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	switch e.Kind {
	case NoCredentials:
		what = "no API key was given"
		if d := oneLine(e.Detail, maxReasonRunes); d != "" {
			what = d
		}
		fix = "pass the API key from a CI secret; pull requests from forks have no secrets, so this is expected there"
	case Auth:
		what = "the API rejected the credentials" + status
		fix = "check that the CI secret holds a valid API key for this API"
	case BadRequest:
		what = "the API refused the request" + status
		if d := oneLine(e.Detail, maxReasonRunes); d != "" {
			what = "the API refused the request (" + strings.TrimSuffix(strings.TrimPrefix(status, " ("), ")") + ": " + d + ")"
			if e.Status == 0 {
				what = "the API refused the request (" + d + ")"
			}
		}
		fix = "check --model and --effort against what the API accepts for your key"
	case RateLimited:
		if e.Detail == "insufficient_quota" {
			what = "the key's quota is used up" + status
			fix = "check billing and quota for the key"
		} else {
			what = "the API rate limit was reached" + status
			fix = "run the job again later"
		}
	case Timeout:
		what = "the API did not answer in time"
		fix = "run the job again; a lower --effort answers faster"
	case Incomplete:
		if e.Detail == "max_output_tokens" {
			what = "the model ran out of output budget before it finished"
			fix = "lower --effort so less of the budget goes to reasoning"
		} else {
			what = "the model stopped before it finished"
			fix = "the provider stopped the answer and these files stay not reviewed; report it if it repeats"
		}
	case Refused:
		what = "the model declined to answer"
		fix = "check that the rules and the instruction ask for a code review"
	case Malformed:
		what = "the answer was not the expected JSON"
		fix = "run the job again; if it repeats, report it with the model name"
	default:
		what = "the API was unavailable" + status
		fix = "run the job again later"
	}
	msg := fmt.Sprintf("ai review stopped: %s; %s not reviewed: %s. Fix: %s", what, count(len(files), "file"), named(files), fix)
	if e.Kind == NoCredentials {
		msg = fmt.Sprintf("ai review skipped: %s; %s not reviewed: %s. Fix: %s", what, count(len(files), "file"), named(files), fix)
	}
	return warn(msg)
}

func warn(msg string) diag.Diagnostic {
	return diag.Diagnostic{Kind: diag.Failure, Sev: diag.Warning, Msg: msg}
}

// named lists the first few files and counts the rest.
func named(files []string) string {
	if len(files) <= namedFiles {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(files[:namedFiles], ", "), len(files)-namedFiles)
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func kib(n int) string { return fmt.Sprintf("%d KiB", n>>10) }

// oneLine makes model or provider text safe to print as one line: control characters become spaces, runs of space
// collapse, and the text is cut to max runes.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-3]) + "..."
	}
	return s
}

func sortDiagnostics(ds []diag.Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Kind != b.Kind {
			return a.Kind > b.Kind
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
}

// detail is the clause the summary line carries about the review.
func (p *Plan) detail(o Outcome, t tally) string {
	effort := p.effort
	if effort == "" {
		effort = "default"
	}
	s := fmt.Sprintf("ai review %s effort %s: %s reviewed, %d not reviewed, %s, %d tokens in, %d out",
		p.model, effort, count(o.Reviewed, "file"), o.NotReviewed, count(o.Calls, "call"), t.in, t.out)
	if t.discarded > 0 {
		s += fmt.Sprintf("; %s discarded (not on a touched line)", count(t.discarded, "model finding"))
	}
	return s
}
