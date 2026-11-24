// Package oai is a small client for the OpenAI-shaped HTTP API that most inference clouds speak: chat completions
// (plain and streamed), image generation, speech, transcription, embeddings and the model list. The cloud drivers
// (vultr, cloudflare, openrouter) build on it and add what their cloud does its own way.
//
// Requests carry an Extra map for provider-specific fields; responses keep their raw JSON so a driver can read fields
// this package doesn't know.
package oai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client calls one OpenAI-compatible endpoint. BaseURL includes the version path, like https://api.openai.com/v1.
type Client struct {
	BaseURL string
	Key     string
	// Header is added to every request (attribution headers, for example).
	Header http.Header
	// HTTP defaults to http.DefaultClient's transport with no overall timeout: a call lasts as long as its context
	// allows, so a long answer that keeps streaming isn't cut off. Callers bound every call with a context deadline.
	HTTP *http.Client
}

var defaultHTTP = &http.Client{}

// Error is a failed call: the HTTP status and the provider's message, from an OpenAI-style error envelope when there
// is one.
type Error struct {
	Status  int
	Code    string
	Message string
	// Body is the start of the raw response body.
	Body string
	// Err is the transport or read error behind it, if any: errors.Is sees context.Canceled and
	// context.DeadlineExceeded through it.
	Err error
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("%d: %s", e.Status, e.Message)
}

// URL is BaseURL joined with path.
func (c *Client) URL(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// Request sends a request and returns the response if its status is 2xx; otherwise it reads the error and closes the
// body. The caller closes a returned body.
func (c *Client) Request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), body)
	if err != nil {
		return nil, &Error{Message: "bad endpoint: " + err.Error()}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, &Error{Message: "no answer from " + req.URL.Host + ": " + err.Error(), Err: err}
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, readError(res)
	}
	return res, nil
}

// JSON sends in as JSON (nil: no body) and decodes a JSON answer into out (nil: discard it).
func (c *Client) JSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	ct := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ct = bytes.NewReader(b), "application/json"
	}
	res, err := c.Request(ctx, method, path, body, ct)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return &Error{Status: res.StatusCode, Message: "reading the answer: " + err.Error(), Err: err}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Status: res.StatusCode, Message: "the answer isn't the JSON expected: " + err.Error(), Body: clip(string(raw), 300)}
	}
	return nil
}

func readError(res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	e := &Error{Status: res.StatusCode, Body: clip(strings.TrimSpace(string(raw)), 300)}
	var env struct {
		Error json.RawMessage `json:"error"`
		// Cloudflare's envelope.
		Errors []struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &env) == nil {
		var obj struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Type    string `json:"type"`
		}
		var s string
		switch {
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &obj) == nil && obj.Message != "":
			e.Message, e.Code = obj.Message, codeString(obj.Code, obj.Type)
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &s) == nil && s != "":
			e.Message = s
		case len(env.Errors) > 0:
			e.Message, e.Code = env.Errors[0].Message, codeString(env.Errors[0].Code, "")
		case env.Message != "":
			e.Message = env.Message
		}
	}
	if e.Message == "" {
		e.Message = e.Body
	}
	if e.Message == "" {
		e.Message = http.StatusText(res.StatusCode)
	}
	return e
}

func codeString(code any, fallback string) string {
	switch c := code.(type) {
	case string:
		if c != "" {
			return c
		}
	case float64:
		return fmt.Sprint(int64(c))
	}
	return fallback
}

// withExtra marshals v and merges extra's keys into the object (extra wins).
func withExtra(v any, extra map[string]any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, x := range extra {
		m[k] = x
	}
	return json.Marshal(m)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
