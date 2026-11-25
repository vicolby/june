// Package reviewtest is the scripted review.Model that tests use in place of a provider, so no test touches the network.
package reviewtest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/vicolby/june/internal/review"
)

// Step is one scripted reply: the answer, or the error when Err is set.
type Step struct {
	Answer review.Answer
	Err    error
}

// Finding is one finding of a reply, as the model reports it.
type Finding struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// Reply is a step that answers with these findings, in the shape the schema asks for.
func Reply(findings ...Finding) Step {
	if findings == nil {
		findings = []Finding{}
	}
	text, err := json.Marshal(struct {
		Findings []Finding `json:"findings"`
	}{findings})
	if err != nil {
		panic("reviewtest: findings are not marshalable: " + err.Error())
	}
	return Step{Answer: review.Answer{Text: string(text), InputTokens: 100, OutputTokens: 10}}
}

// Text is a step that answers with raw text, for a malformed reply.
func Text(s string) Step { return Step{Answer: review.Answer{Text: s}} }

// Fail is a step that fails the call with err.
func Fail(err error) Step { return Step{Err: err} }

// Fake plays its steps in order and records every query. A call past the last step fails the call.
type Fake struct {
	Steps []Step

	mu      sync.Mutex
	queries []review.Query
}

// NewFake returns a Fake that plays steps in order.
func NewFake(steps ...Step) *Fake { return &Fake{Steps: steps} }

// Complete records q and plays the next step.
func (f *Fake) Complete(ctx context.Context, q review.Query) (review.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if err := ctx.Err(); err != nil {
		return review.Answer{}, err
	}
	n := len(f.queries) - 1
	if n >= len(f.Steps) {
		return review.Answer{}, fmt.Errorf("reviewtest: call %d has no scripted step", n+1)
	}
	s := f.Steps[n]
	return s.Answer, s.Err
}

// Queries is every query received so far.
func (f *Fake) Queries() []review.Query {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]review.Query(nil), f.queries...)
}
