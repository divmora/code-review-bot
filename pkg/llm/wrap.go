package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WithRetry retries retryable failures with exponential backoff and jitter,
// honouring Retry-After when the provider sends it.
func WithRetry(c Client, retries int, logger *slog.Logger) Client {
	return &retrying{Client: c, retries: retries, logger: logger}
}

type retrying struct {
	Client
	retries int
	logger  *slog.Logger
}

func (r *retrying) Complete(ctx context.Context, req Request) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt <= r.retries; attempt++ {
		resp, err := r.Client.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil || !IsRetryable(err) || attempt == r.retries {
			break
		}
		wait := time.Duration(1<<attempt) * time.Second
		wait += time.Duration(rand.Int64N(int64(wait / 2)))
		var ae *APIError
		if errors.As(err, &ae) && ae.RetryAfter > 0 {
			wait = min(ae.RetryAfter, 60*time.Second)
		}
		r.logger.Warn("llm request failed; retrying", "model", r.Name(), "attempt", attempt+1, "wait", wait, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

type fallback struct {
	clients []Client
	logger  *slog.Logger
}

func (f *fallback) Name() string {
	names := make([]string, len(f.clients))
	for i, c := range f.clients {
		names[i] = c.Name()
	}
	return strings.Join(names, " -> ")
}

func (f *fallback) Complete(ctx context.Context, req Request) (*Response, error) {
	var errs []error
	for i, c := range f.clients {
		resp, err := c.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", c.Name(), err))
		if i < len(f.clients)-1 {
			f.logger.Warn("llm model failed; falling back", "from", c.Name(), "to", f.clients[i+1].Name(), "error", err)
		}
	}
	return nil, errors.Join(errs...)
}

// ExtractJSON finds the first JSON object in model text. It accepts bare
// JSON, ```json fenced blocks, and JSON surrounded by prose.
func ExtractJSON(text string) (json.RawMessage, error) {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "```"); i >= 0 {
		rest := t[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			if raw, err := firstObject(rest[:j]); err == nil {
				return raw, nil
			}
		}
	}
	return firstObject(t)
}

func firstObject(s string) (json.RawMessage, error) {
	for start := strings.IndexByte(s, '{'); start >= 0; {
		dec := json.NewDecoder(strings.NewReader(s[start:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil {
			return raw, nil
		}
		next := strings.IndexByte(s[start+1:], '{')
		if next < 0 {
			break
		}
		start += next + 1
	}
	return nil, ErrNoJSON
}

// schemaInstruction is appended to the system prompt for backends that have
// no native structured output.
func schemaInstruction(s *Schema) string {
	b, _ := json.MarshalIndent(s.JSON, "", "  ")
	return "\n\nRespond with a single JSON object only, no prose and no code fences. It must match this JSON Schema:\n" + string(b)
}

// httpJSON posts a JSON body and decodes a JSON response, turning non-2xx
// statuses into *APIError.
func httpJSON(ctx context.Context, hc *http.Client, provider, url string, headers map[string]string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s request: %w", provider, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("%s read response: %w", provider, err)
	}
	if resp.StatusCode/100 != 2 {
		ae := &APIError{Provider: provider, StatusCode: resp.StatusCode, Body: string(data)}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			ae.RetryAfter = time.Duration(s) * time.Second
		}
		return ae
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s decode response: %w", provider, err)
	}
	return nil
}

func mergeHeaders(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
