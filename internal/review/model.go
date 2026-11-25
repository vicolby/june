package review

import (
	"context"
	"encoding/json"
	"fmt"
)

// Model is the one seam of this package: a provider that answers one query.
// The OpenAI adapter is the production implementation and reviewtest.Fake the scripted one.
// An error that is not a *CallError is reported as an unavailable model; cancellation is returned as it is.
type Model interface {
	Complete(ctx context.Context, q Query) (Answer, error)
}

// Query is one call: instructions, the input they apply to and the shape the answer must have.
type Query struct {
	Model, Effort string
	// Instructions is the instruction and the output contract.
	Instructions string
	// Input is the rules and the windows of source.
	Input string
	// Schema is the JSON Schema the answer text must satisfy.
	Schema          json.RawMessage
	MaxOutputTokens int
}

// Answer is the text a model returned and what it cost.
type Answer struct {
	Text                      string
	InputTokens, OutputTokens int
}

// Kind says why a call failed, so that the warning can say what to do about it.
type Kind int

const (
	// NoCredentials means there was no key to call with, so no request was made.
	NoCredentials Kind = iota
	// Auth means the provider rejected the credentials.
	Auth
	// BadRequest means the provider refused the request: an unknown model, an unsupported effort, input too long.
	BadRequest
	// RateLimited means the provider asked to slow down, and retries did not help.
	RateLimited
	// Unavailable means a server error or a network failure, after retries.
	Unavailable
	// Timeout means the call did not finish in the time that was left.
	Timeout
	// Incomplete means the output budget ran out before the answer was complete.
	Incomplete
	// Refused means the model declined to answer.
	Refused
	// Malformed means the answer was not the JSON the schema asks for.
	Malformed
)

// CallError is a failed call. Detail is text the provider adapter has already scrubbed of credentials.
type CallError struct {
	Kind Kind
	// Status is the HTTP status, 0 when there was none.
	Status int
	Detail string
}

func (e *CallError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("review: model call failed (kind %d, status %d)", e.Kind, e.Status)
	}
	return fmt.Sprintf("review: model call failed (kind %d, status %d): %s", e.Kind, e.Status, e.Detail)
}
