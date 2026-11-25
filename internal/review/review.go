// Package review asks a model whether the lines a change touched break written guideline rules.
//
// The rules are one Markdown file that this package hands to the model as it is: it parses no rule, gives none an id
// and loads nothing else. What it does own is everything around that text: the instruction and the output contract,
// which lines of which files are sent, the size budgets, the answer's schema, and the guard that drops any finding
// the model reports outside the lines the change touched. Every finding is a warning, since a model cannot be certain.
//
// The package imports no network package. A provider is a Model that the caller passes in.
package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vicolby/june/internal/diag"
)

// Code is the rule code of every finding, which has no rule id of its own.
const Code = "AI"

// Limits bound what one run sends and spends. A zero field means its default.
type Limits struct {
	// TotalBytes is the source a run sends in all calls together, default 256 KiB.
	TotalBytes int
	// CallBytes is the source of one call, default 64 KiB.
	CallBytes int
	// FileBytes is the largest window of one file, default 48 KiB. A bigger one is skipped whole, never truncated.
	FileBytes int
	// MaxCalls is the most calls a run makes, default 4.
	MaxCalls int
	// OutputTokens is the output cap of a call, reasoning included, default 16000.
	OutputTokens int
}

func (l Limits) withDefaults() Limits {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&l.TotalBytes, 256<<10)
	def(&l.CallBytes, 64<<10)
	def(&l.FileBytes, 48<<10)
	def(&l.MaxCalls, 4)
	def(&l.OutputTokens, 16000)
	return l
}

const (
	maxRulesBytes = 64 << 10
	maxSkillBytes = 32 << 10
)

// Config says what to review against and how to ask.
type Config struct {
	// Rules is the path of the one Markdown file of rules. It is required.
	Rules string
	// Skill is a skill directory (its SKILL.md) or a skill file whose body replaces the default instruction.
	Skill string
	// Prompt is an instruction that replaces the default one. It cannot be combined with Skill.
	Prompt string
	// Model is the provider's model name. It is required, and has no default.
	Model string
	// Effort is the reasoning effort, passed to the API as given. Empty leaves it to the model's default.
	Effort string
	Limits Limits
}

// Plan is a validated Config with its files read.
type Plan struct {
	model, effort string
	rules         string
	instructions  string
	limits        Limits
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,99}$`)

// Prepare validates cfg and reads the files it names, relative to dir. A fault is a complete "<cause>. Fix: <instruction>"
// refusal; any fault means there is no Plan, and all of them are reported at once.
func Prepare(dir string, cfg Config) (*Plan, []string) {
	var faults []string
	fault := func(cause, fix string) { faults = append(faults, cause+". Fix: "+fix) }

	p := &Plan{model: strings.TrimSpace(cfg.Model), effort: cfg.Effort, limits: cfg.Limits.withDefaults()}
	switch {
	case p.model == "":
		fault("--model is required, and june review has no default model", "name a model your API key can use, such as --model MODEL")
	case !modelName.MatchString(p.model):
		fault(fmt.Sprintf("--model %q is not a model name", p.model), "pass the model name as the provider spells it")
	}
	if strings.TrimSpace(cfg.Rules) == "" {
		fault("--rules is required", "pass the Markdown file that holds the rules to check, --rules PATH")
	} else if text, f := readText(dir, cfg.Rules, "rules file", maxRulesBytes, "64 KiB"); f != "" {
		faults = append(faults, f)
	} else {
		p.rules = text
	}

	instruction := defaultInstruction
	prompt := strings.TrimSpace(cfg.Prompt)
	switch {
	case cfg.Skill != "" && prompt != "":
		fault("--skill and --prompt cannot be used together", "pass one of them: --skill PATH to read the instruction from a skill, or --prompt TEXT to give it inline")
	case cfg.Skill != "":
		body, f := readSkill(dir, cfg.Skill)
		if f != "" {
			faults = append(faults, f)
		} else {
			instruction = body
		}
	case prompt != "":
		instruction = prompt
	}
	if len(faults) > 0 {
		return nil, faults
	}
	p.instructions = instructions(instruction)
	return p, nil
}

// readText reads a UTF-8 text file of at most max bytes and refuses an empty one.
func readText(dir, path, what string, max int, maxLabel string) (string, string) {
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(dir, path)
	}
	info, err := os.Stat(full)
	switch {
	case err != nil:
		return "", fmt.Sprintf("the %s %s cannot be read (%s). Fix: pass an existing file", what, path, reason(err))
	case info.IsDir():
		return "", fmt.Sprintf("the %s %s is a directory. Fix: pass one Markdown file", what, path)
	case !info.Mode().IsRegular():
		return "", fmt.Sprintf("the %s %s is not a regular file. Fix: pass one Markdown file", what, path)
	case info.Size() > int64(max):
		return "", fmt.Sprintf("the %s %s is over %s. Fix: shorten it, because all of it is sent with every call", what, path, maxLabel)
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Sprintf("the %s %s cannot be read (%s). Fix: pass an existing file", what, path, reason(err))
	}
	if len(raw) > max {
		return "", fmt.Sprintf("the %s %s is over %s. Fix: shorten it, because all of it is sent with every call", what, path, maxLabel)
	}
	if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return "", fmt.Sprintf("the %s %s is not UTF-8 text. Fix: save it as UTF-8", what, path)
	}
	text := string(raw)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Sprintf("the %s %s is empty. Fix: write the rules into it", what, path)
	}
	return text, ""
}

// reason is the part of a file error that helps a reader, without the path the message already names.
func reason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// readSkill reads the instruction of a skill: the body of SKILL.md in a directory, or of the file itself.
// Its front matter is dropped, and anything else the skill bundles is ignored.
func readSkill(dir, path string) (string, string) {
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(dir, path)
	}
	file := path
	if info, err := os.Stat(full); err == nil && info.IsDir() {
		file = filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(filepath.Join(full, "SKILL.md")); err != nil && os.IsNotExist(err) {
			return "", fmt.Sprintf("the skill directory %s has no SKILL.md. Fix: pass the skill directory that holds SKILL.md, or the skill file", path)
		}
	}
	text, fault := readText(dir, file, "skill", maxSkillBytes, "32 KiB")
	if fault != "" {
		return "", fault
	}
	body, closed := frontMatter(text)
	if !closed {
		return "", fmt.Sprintf("the front matter of skill %s is never closed. Fix: end it with a line holding only ---", file)
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Sprintf("skill %s has no instruction after its front matter. Fix: write the instruction in the skill body", file)
	}
	return body, ""
}

// Source is one changed file: its decoded text and the lines the change touched, which are the only ones a finding may land on.
type Source struct {
	Path string
	// Text is UTF-8.
	Text    string
	Touched []Span
}

// Span is a range of lines, numbered from 1, both ends included.
type Span struct{ From, To int }

// Outcome is what a run produced.
type Outcome struct {
	// Diags are the findings, all warnings, and a warning for everything that went unreviewed or failed.
	Diags []diag.Diagnostic
	// Detail is the sentence the summary line carries about the review.
	Detail string
	// Reviewed counts the files a model answered for, NotReviewed the files that were left out for any reason.
	Reviewed, NotReviewed, Calls int
}

// Run reviews files with m. It never makes a finding an error and never fails the run for a failed call:
// a call that fails becomes a warning naming the files it left unreviewed, and ends the run.
// The error is only the cancellation of ctx; a deadline is a timeout warning.
func (p *Plan) Run(ctx context.Context, m Model, files []Source) (Outcome, error) {
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return Outcome{}, err
	}
	var out Outcome
	var skippedOutside, skippedLarge, skippedBudget []string
	windows := p.windows(files, &skippedOutside, &skippedLarge)
	batches, over := p.batch(windows)
	for _, w := range over {
		skippedBudget = append(skippedBudget, w.path)
	}

	var t tally
	var unreviewed []string
	for i, b := range batches {
		if err := ctx.Err(); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return Outcome{}, err
			}
			unreviewed = append(unreviewed, paths(batches[i:])...)
			out.Diags = append(out.Diags, failure(&CallError{Kind: Timeout}, unreviewed))
			break
		}
		findings, err := p.ask(ctx, m, b, &t)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil && !errors.Is(cerr, context.DeadlineExceeded) {
				return Outcome{}, cerr
			}
			unreviewed = append(unreviewed, paths(batches[i:])...)
			out.Diags = append(out.Diags, failure(asCallError(ctx, err), unreviewed))
			break
		}
		t.reviewed += len(b.windows)
		out.Diags = append(out.Diags, findings...)
	}
	if len(skippedOutside) > 0 {
		out.Diags = append(out.Diags, warn(fmt.Sprintf("ai review left out %s whose changed lines are not in the file: %s. Fix: regenerate the changed-lines list from the current tree",
			count(len(skippedOutside), "file"), named(skippedOutside))))
	}
	if len(skippedLarge) > 0 {
		out.Diags = append(out.Diags, warn(fmt.Sprintf("ai review skipped %s whose changed lines and their context are over the %s limit of one file: %s. Fix: split the file, or keep it out of the review with --exclude",
			count(len(skippedLarge), "file"), kib(p.limits.fileBytes()), named(skippedLarge))))
	}
	if len(skippedBudget) > 0 {
		out.Diags = append(out.Diags, warn(fmt.Sprintf("ai review left out %s because the change is over its budget of %s of source in at most %s: %s. Fix: review smaller pull requests, or narrow the run with PATH or --exclude",
			count(len(skippedBudget), "file"), kib(p.limits.TotalBytes), count(p.limits.MaxCalls, "call"), named(skippedBudget))))
	}
	out.Reviewed, out.Calls = t.reviewed, t.calls
	out.NotReviewed = len(skippedOutside) + len(skippedLarge) + len(skippedBudget) + len(unreviewed)
	sortDiagnostics(out.Diags)
	out.Detail = p.detail(out, t)
	return out, nil
}
