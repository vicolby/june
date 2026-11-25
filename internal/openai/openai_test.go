package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vicolby/june/internal/openai"
	"github.com/vicolby/june/internal/review"
)

const key = "sk-proj-TESTKEYTESTKEYTESTKEY"

var query = review.Query{
	Model:           "test-model",
	Effort:          "low",
	Instructions:    "Review the code.",
	Input:           "FILE a.py\n> 1| x = 1\n",
	Schema:          json.RawMessage(`{"type":"object","additionalProperties":false,"required":["findings"],"properties":{"findings":{"type":"array"}}}`),
	MaxOutputTokens: 1234,
}

const okBody = `{"status":"completed","output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"{\"findings\":[]}"}]}],"usage":{"input_tokens":812,"output_tokens":96}}`

// rewriteOpenAI sends the adapter's fixed OpenAI request to a local server.
// A request to any other host fails the test: the adapter has no endpoint setting.
func rewriteOpenAI(t *testing.T, serverURL string) http.RoundTripper {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" || !strings.HasPrefix(req.URL.Path, "/v1/") {
			t.Errorf("request URL = %s, want https://api.openai.com/v1/...", req.URL)
			return nil, fmt.Errorf("request %s is not the OpenAI API", req.URL)
		}
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		cloned.Host = target.Host
		return http.DefaultTransport.RoundTrip(cloned)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestClient(t *testing.T, cfg openai.Config, serverURL string) *openai.Client {
	t.Helper()
	cfg.Client = &http.Client{Transport: rewriteOpenAI(t, serverURL)}
	c, err := openai.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// serve runs h as the API and returns a client for it with fast retries.
func serve(t *testing.T, h http.HandlerFunc) (*openai.Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, openai.Config{APIKey: key, UserAgent: "june/test", RetryDelay: time.Millisecond}, srv.URL)
	return c, &n
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func callErr(t *testing.T, err error) *review.CallError {
	t.Helper()
	var ce *review.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a *review.CallError", err)
	}
	return ce
}

func TestARequestCarriesTheDocumentedShape(t *testing.T) {
	var got struct {
		Method, Path, Auth, UA, ContentType string
		Body                                map[string]any
	}
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got.Method, got.Path, got.Auth, got.UA, got.ContentType = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got.Body); err != nil {
			t.Error(err)
		}
		reply(200, okBody)(w, r)
	})
	ans, err := c.Complete(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" || got.Path != "/v1/responses" || got.Auth != "Bearer "+key || got.UA != "june/test" || got.ContentType != "application/json" {
		t.Errorf("request = %+v", got)
	}
	b := got.Body
	if b["model"] != "test-model" || b["instructions"] != "Review the code." || b["input"] != query.Input {
		t.Errorf("body = %v", b)
	}
	if b["store"] != false {
		t.Errorf("store = %v, want false: nothing is kept on the provider's side", b["store"])
	}
	if b["max_output_tokens"] != float64(1234) {
		t.Errorf("max_output_tokens = %v", b["max_output_tokens"])
	}
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "low" {
		t.Errorf("reasoning = %v", b["reasoning"])
	}
	for _, banned := range []string{"tools", "temperature", "tool_choice", "previous_response_id"} {
		if _, ok := b[banned]; ok {
			t.Errorf("body has %q, but the model must only answer with JSON", banned)
		}
	}
	format, _ := b["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "june_review" {
		t.Errorf("text.format = %v", format)
	}
	schema, _ := json.Marshal(format["schema"])
	var want any
	_ = json.Unmarshal(query.Schema, &want)
	wantJSON, _ := json.Marshal(want)
	if string(schema) != string(wantJSON) {
		t.Errorf("schema = %s, want %s", schema, wantJSON)
	}
	if ans.Text != `{"findings":[]}` || ans.InputTokens != 812 || ans.OutputTokens != 96 {
		t.Errorf("answer = %+v", ans)
	}
}

func TestNoEffortOmitsTheReasoningKey(t *testing.T) {
	var body map[string]any
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(200, okBody)(w, r)
	})
	q := query
	q.Effort = ""
	if _, err := c.Complete(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning"]; ok {
		t.Errorf("body = %v, want no reasoning key", body)
	}
}

func TestWithoutAKeyNothingIsSent(t *testing.T) {
	for _, k := range []string{"", "  \n"} {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { n.Add(1) }))
		t.Cleanup(srv.Close)
		c := newTestClient(t, openai.Config{APIKey: k}, srv.URL)
		_, err := c.Complete(context.Background(), query)
		if ce := callErr(t, err); ce.Kind != review.NoCredentials || !strings.Contains(ce.Detail, "OPENAI_API_KEY is not set") {
			t.Errorf("err = %+v", ce)
		}
		if n.Load() != 0 {
			t.Errorf("%d requests were made without a key", n.Load())
		}
	}
}

func TestAKeyWithTrailingNewlineIsTrimmed(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		reply(200, okBody)(w, r)
	}))
	defer srv.Close()
	c := newTestClient(t, openai.Config{APIKey: key + "\n"}, srv.URL)
	if _, err := c.Complete(context.Background(), query); err != nil || auth != "Bearer "+key {
		t.Errorf("auth = %q, err = %v", auth, err)
	}
}

func TestAKeyWithInnerWhitespaceIsAnAuthFailureNotACrash(t *testing.T) {
	c, err := openai.New(openai.Config{
		APIKey: "sk-a b\x01c",
		Client: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
			t.Error("a request was sent")
			return nil, errors.New("no network")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), query)
	ce := callErr(t, err)
	if ce.Kind != review.Auth || strings.Contains(err.Error(), "sk-a") {
		t.Errorf("err = %v", err)
	}
}

func TestFailuresMapToKindsAndNeverEchoTheKey(t *testing.T) {
	echo := func(msg string) string {
		return fmt.Sprintf(`{"error":{"message":%q,"type":"x","code":"some_code"}}`, msg)
	}
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		kind    review.Kind
		detail  string // a substring of Detail
		retries int32  // requests made in all
	}{
		{"unauthorized", 401, echo("Incorrect API key provided: " + key), review.Auth, "", 1},
		{"forbidden", 403, echo("no access " + key), review.Auth, "", 1},
		{"bad request", 400, echo("Unsupported value: 'low' is not supported with this model."), review.BadRequest, "Unsupported value", 1},
		{"unknown model", 404, echo("The model `nope` does not exist"), review.BadRequest, "does not exist", 1},
		{"unprocessable", 422, echo("bad"), review.BadRequest, "bad", 1},
		{"bad request echoing the key", 400, echo("bad key " + key + " and sk-other-ABCDEFGHIJKL"), review.BadRequest, "[redacted]", 1},
		{"rate limited", 429, echo("slow down"), review.RateLimited, "", 3},
		{"out of quota", 429, `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`, review.RateLimited, "", 1},
		{"server error", 500, echo("oops " + key), review.Unavailable, "", 3},
		{"bad gateway with html", 502, "<html>" + key + "</html>", review.Unavailable, "", 3},
		{"service unavailable", 503, "", review.Unavailable, "", 3},
		{"bad request without json", 400, "plain " + key, review.BadRequest, "", 1},
		{"redirect", 307, "", review.Unavailable, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, n := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 307 {
					w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
				}
				reply(tc.status, tc.body)(w, r)
			})
			_, err := c.Complete(context.Background(), query)
			ce := callErr(t, err)
			if ce.Kind != tc.kind || ce.Status != tc.status {
				t.Errorf("kind, status = %d, %d; want %d, %d", ce.Kind, ce.Status, tc.kind, tc.status)
			}
			if !strings.Contains(ce.Detail, tc.detail) {
				t.Errorf("Detail = %q, want it to contain %q", ce.Detail, tc.detail)
			}
			if tc.kind == review.Auth && ce.Detail != "" {
				t.Errorf("Detail = %q: the body of a 401 or 403 is never echoed", ce.Detail)
			}
			for _, s := range []string{err.Error(), ce.Detail} {
				if strings.Contains(s, key) || strings.Contains(s, "sk-other") || strings.Contains(s, "TESTKEY") {
					t.Errorf("the key leaked into %q", s)
				}
			}
			if n.Load() != tc.retries {
				t.Errorf("requests = %d, want %d", n.Load(), tc.retries)
			}
		})
	}
}

func TestAnErrorMessageIsOneShortLine(t *testing.T) {
	c, _ := serve(t, reply(400, `{"error":{"message":"first\nsecond `+strings.Repeat("x", 1000)+`"}}`))
	_, err := c.Complete(context.Background(), query)
	ce := callErr(t, err)
	if strings.ContainsAny(ce.Detail, "\n\r") || len([]rune(ce.Detail)) > 300 {
		t.Errorf("Detail = %q", ce.Detail)
	}
}

func TestARateLimitIsRetriedAndHonoursRetryAfter(t *testing.T) {
	var calls atomic.Int32
	c, n := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			reply(429, `{"error":{"message":"slow"}}`)(w, r)
			return
		}
		reply(200, okBody)(w, r)
	})
	if _, err := c.Complete(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Errorf("requests = %d, want 2", n.Load())
	}
}

func TestARetryNeverOutlivesTheCallersDeadline(t *testing.T) {
	c, n := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		reply(429, "")(w, r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, query) // an hour of Retry-After would hang here if the wait ignored the deadline
	if ce := callErr(t, err); ce.Kind != review.Timeout {
		t.Errorf("err = %v, want a timeout", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
}

func TestANetworkFailureIsRetriedThenUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := srv.URL
	srv.Close()
	c := newTestClient(t, openai.Config{APIKey: key, RetryDelay: time.Millisecond}, serverURL)
	_, err := c.Complete(context.Background(), query)
	ce := callErr(t, err)
	if ce.Kind != review.Unavailable || strings.Contains(err.Error(), key) || strings.Contains(ce.Detail, "127.0.0.1") {
		t.Errorf("err = %v, detail %q", err, ce.Detail)
	}
}

func TestADeadlineIsATimeoutAndCancellationIsCancellation(t *testing.T) {
	block := make(chan struct{})
	c, _ := serve(t, func(http.ResponseWriter, *http.Request) { <-block })
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, query)
	if ce := callErr(t, err); ce.Kind != review.Timeout {
		t.Errorf("err = %v, want a timeout", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := c.Complete(ctx, query); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestAnswersThatAreNotAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		kind       review.Kind
		detail     string
	}{
		{"incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, review.Incomplete, "max_output_tokens"},
		{"refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I cannot help with that."}]}]}`, review.Refused, ""},
		{"no output", `{"status":"completed","output":[]}`, review.Malformed, ""},
		{"only reasoning", `{"status":"completed","output":[{"type":"reasoning"}]}`, review.Malformed, ""},
		{"not JSON", `<html>`, review.Malformed, ""},
		{"empty", ``, review.Malformed, ""},
		{"failed", `{"status":"failed","error":{"code":"server_error","message":"boom"}}`, review.Unavailable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := serve(t, reply(200, tc.body))
			_, err := c.Complete(context.Background(), query)
			ce := callErr(t, err)
			if ce.Kind != tc.kind || !strings.Contains(ce.Detail, tc.detail) {
				t.Errorf("err = %+v, want kind %d with %q", ce, tc.kind, tc.detail)
			}
		})
	}
}

func TestSeveralTextPartsAreJoined(t *testing.T) {
	c, _ := serve(t, reply(200, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"findings\":"},{"type":"output_text","text":"[]}"}]}]}`))
	ans, err := c.Complete(context.Background(), query)
	if err != nil || ans.Text != `{"findings":[]}` {
		t.Errorf("answer = %+v, err = %v", ans, err)
	}
}

func TestACredentialIsNeverSentToARedirectTarget(t *testing.T) {
	var seen atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		reply(200, okBody)(w, r)
	}))
	defer other.Close()
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/elsewhere", http.StatusTemporaryRedirect)
	})
	if _, err := c.Complete(context.Background(), query); err == nil {
		t.Errorf("a redirect must not count as an answer")
	}
	if seen.Load() != 0 {
		t.Errorf("the redirect target received %d requests", seen.Load())
	}
}

func TestAHugeBodyIsCutOff(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < 20; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	})
	_, err := c.Complete(context.Background(), query)
	if ce := callErr(t, err); ce.Kind != review.Malformed {
		t.Errorf("err = %v", err)
	}
}

// Whatever status and body a server answers with, no error text carries the key.
func TestNoStatusCanLeakTheKey(t *testing.T) {
	for status := 100; status < 600; status++ {
		if status == 101 || status == 204 || status == 205 || status == 304 || status/100 == 1 {
			continue
		}
		c, _ := serve(t, reply(status, `{"error":{"message":"`+key+`"}}`+key))
		_, err := c.Complete(context.Background(), query)
		if err == nil {
			continue
		}
		ce := callErr(t, err)
		if strings.Contains(err.Error(), "TESTKEY") || strings.Contains(ce.Detail, "TESTKEY") {
			t.Errorf("status %d leaked the key: %v / %q", status, err, ce.Detail)
		}
	}
}
