// Package openai is the OpenAI adapter of review.Model: one call of the Responses API with a strict JSON-schema answer.
// It is the only package that opens a connection, and it uses the standard library alone.
//
// The key is never put in an error. Anything the API says is scrubbed of it first, the body of a 401 or a 403 is not
// echoed at all, and the request is never repeated at another address: a redirect is a failure.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/vicolby/june/internal/review"
)

// apiRoot is the only address a request is sent to.
const apiRoot = "https://api.openai.com/v1"

const (
	maxRetries      = 2
	maxRetryAfter   = 30 * time.Second
	maxAnswerBytes  = 8 << 20
	maxErrorBytes   = 64 << 10
	maxDetailRunes  = 300
	redacted        = "[redacted]"
	defaultDelay    = time.Second
	schemaName      = "june_review"
	defaultUserText = "june"
)

// Config configures a Client.
type Config struct {
	// APIKey is the bearer credential. Empty makes every call fail with review.NoCredentials, without a request.
	APIKey string
	// UserAgent is sent with every request; empty means "june".
	UserAgent string
	// Client sends the requests; nil means a new http.Client. Its redirects are always refused.
	Client *http.Client
	// RetryDelay is the first wait before a retry, doubling each time; zero means one second.
	RetryDelay time.Duration
}

// Client is a review.Model that calls the OpenAI Responses API.
type Client struct {
	key, base, agent string
	http             *http.Client
	delay            time.Duration
}

// New builds a client. It never touches the network, and a bad key is not an error here: it fails the call that uses it.
// Requests go to https://api.openai.com/v1 and nowhere else.
func New(cfg Config) (*Client, error) {
	hc := &http.Client{}
	if cfg.Client != nil {
		copied := *cfg.Client
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{key: strings.TrimSpace(cfg.APIKey), base: apiRoot, agent: cfg.UserAgent, http: hc, delay: cfg.RetryDelay}
	if c.agent == "" {
		c.agent = defaultUserText
	}
	if c.delay <= 0 {
		c.delay = defaultDelay
	}
	return c, nil
}

type request struct {
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions"`
	Input           string     `json:"input"`
	Reasoning       *reasoning `json:"reasoning,omitempty"`
	Text            text       `json:"text"`
	MaxOutputTokens int        `json:"max_output_tokens"`
	Store           bool       `json:"store"`
}

type reasoning struct {
	Effort string `json:"effort"`
}

type text struct {
	Format format `json:"format"`
}

type format struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type response struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// failure is a failed attempt: the error to report, and whether another attempt may help.
type failure struct {
	err   *review.CallError
	retry bool
	after time.Duration
}

// Complete makes one call, retrying a rate limit, a server error or a network failure twice.
// Every error is a *review.CallError except the cancellation of ctx, which is returned as it is.
func (c *Client) Complete(ctx context.Context, q review.Query) (review.Answer, error) {
	if c.key == "" {
		return review.Answer{}, &review.CallError{Kind: review.NoCredentials, Detail: "OPENAI_API_KEY is not set"}
	}
	if strings.IndexFunc(c.key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return review.Answer{}, &review.CallError{Kind: review.Auth, Detail: "the API key holds whitespace or control characters"}
	}
	body := request{
		Model: q.Model, Instructions: q.Instructions, Input: q.Input, MaxOutputTokens: q.MaxOutputTokens,
		Text: text{format{Type: "json_schema", Name: schemaName, Strict: true, Schema: q.Schema}},
	}
	if q.Effort != "" {
		body.Reasoning = &reasoning{Effort: q.Effort}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return review.Answer{}, &review.CallError{Kind: review.BadRequest, Detail: "the request cannot be encoded"}
	}
	for attempt := 0; ; attempt++ {
		ans, fail := c.once(ctx, payload)
		if fail == nil {
			return ans, nil
		}
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return review.Answer{}, &review.CallError{Kind: review.Timeout}
			}
			return review.Answer{}, err
		}
		if !fail.retry || attempt == maxRetries {
			return review.Answer{}, fail.err
		}
		wait := c.delay << attempt
		if fail.after > 0 {
			wait = min(fail.after, maxRetryAfter)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return review.Answer{}, &review.CallError{Kind: review.Timeout}
			}
			return review.Answer{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// once sends the request one time.
func (c *Client) once(ctx context.Context, payload []byte) (review.Answer, *failure) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/responses", bytes.NewReader(payload))
	if err != nil {
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.BadRequest, Detail: "the request cannot be built"}}
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.agent)
	res, err := c.http.Do(req)
	if err != nil {
		// The text of a transport error can name the address, so it is never reported.
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.Unavailable}, retry: true}
	}
	defer func() { _ = res.Body.Close() }() // The body is fully read or abandoned; a close error changes nothing.
	if res.StatusCode != http.StatusOK {
		return review.Answer{}, c.statusFailure(res)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes+1))
	if err != nil || len(raw) > maxAnswerBytes {
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.Malformed, Status: res.StatusCode}}
	}
	return c.answer(raw)
}

func billingDetail(code, typ string) string {
	switch code {
	case "insufficient_quota":
		return "insufficient_quota"
	case "credit_balance_exhausted", "organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded":
		return "billing_limit"
	}
	if typ == "insufficient_quota" {
		return "billing_limit"
	}
	return ""
}

// statusFailure maps a status that is not 200 to what the user can do about it.
func (c *Client) statusFailure(res *http.Response) *failure {
	status := res.StatusCode
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes)) // An unreadable error body only loses the detail.
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env) // Not JSON means no detail.
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &failure{err: &review.CallError{Kind: review.Auth, Status: status}}
	case status == http.StatusTooManyRequests:
		if detail := billingDetail(env.Error.Code, env.Error.Type); detail != "" {
			return &failure{err: &review.CallError{Kind: review.RateLimited, Status: status, Detail: detail}}
		}
		f := &failure{err: &review.CallError{Kind: review.RateLimited, Status: status}, retry: true}
		if secs, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && secs >= 0 {
			f.after = time.Duration(secs) * time.Second
			if f.after == 0 {
				f.after = time.Nanosecond // an explicit zero is an immediate retry, not "use the default"
			}
		}
		return f
	case status >= 500:
		return &failure{err: &review.CallError{Kind: review.Unavailable, Status: status}, retry: true}
	case status >= 400:
		return &failure{err: &review.CallError{Kind: review.BadRequest, Status: status, Detail: c.scrub(env.Error.Message)}}
	}
	// A redirect or any other status: not an answer, and not worth another request.
	return &failure{err: &review.CallError{Kind: review.Unavailable, Status: status}}
}

// answer reads the text out of a 200 response.
func (c *Client) answer(raw []byte) (review.Answer, *failure) {
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.Malformed, Status: http.StatusOK}}
	}
	switch r.Status {
	case "incomplete":
		reason := ""
		if r.IncompleteDetails != nil {
			reason = c.scrub(r.IncompleteDetails.Reason)
		}
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.Incomplete, Status: http.StatusOK, Detail: reason}}
	case "failed", "cancelled":
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.Unavailable, Status: http.StatusOK}}
	}
	var out strings.Builder
	refused := false
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				out.WriteString(part.Text)
			case "refusal":
				refused = true
			}
		}
	}
	switch {
	case out.Len() > 0:
		return review.Answer{Text: out.String(), InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}, nil
	case refused:
		return review.Answer{}, &failure{err: &review.CallError{Kind: review.Refused, Status: http.StatusOK}}
	}
	return review.Answer{}, &failure{err: &review.CallError{Kind: review.Malformed, Status: http.StatusOK}}
}

var keyShape = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)

// scrub makes text from the API safe to print: no key, one line, short.
func (c *Client) scrub(s string) string {
	if c.key != "" {
		s = strings.ReplaceAll(s, c.key, redacted)
	}
	s = keyShape.ReplaceAllString(s, redacted)
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > maxDetailRunes {
		s = string(r[:maxDetailRunes-3]) + "..."
	}
	return s
}
