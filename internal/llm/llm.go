// Package llm is a small provider-neutral client for chat models.
//
// Every backend (OpenAI and compatible servers, Anthropic, Google Gemini,
// AWS Bedrock) implements Client. Callers pick a backend with a ModelConfig,
// usually parsed from a spec string such as "bedrock:<model-id>" or a named
// profile in the config file, so switching providers is a config change.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Role of a chat message.
type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
)

// Message is one chat turn. The system prompt is passed separately.
type Message struct {
	Role    Role
	Content string
}

// Schema asks the model for a JSON object matching a JSON Schema. Each
// backend uses its native mechanism (response_format, forced tool use,
// responseJsonSchema) and the parsed object is returned in Response.JSON.
type Schema struct {
	Name        string
	Description string
	JSON        map[string]any
}

// Request is a single completion request.
type Request struct {
	System      string
	Messages    []Message
	MaxTokens   int      // 0 means the model config default
	Temperature *float64 // nil means the provider default
	Schema      *Schema
}

// Usage reports token counts when the provider returns them.
type Usage struct {
	InputTokens  int // all prompt tokens, including cache hits
	OutputTokens int
	// CachedInputTokens is the part of InputTokens served from a prompt
	// cache (billed at a lower rate), when the provider reports it.
	CachedInputTokens int
}

// Response is a completion result.
type Response struct {
	Text       string
	JSON       json.RawMessage // set when Request.Schema was given
	Model      string
	StopReason string
	Usage      Usage
}

// Client is implemented by every provider.
type Client interface {
	// Name identifies the client in logs, e.g. "openai:gpt-x".
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

// APIError is an HTTP-level failure from a provider.
type APIError struct {
	Provider   string
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, body)
}

// Retryable reports whether the request may succeed if sent again.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode == 408 || e.StatusCode == 409 || e.StatusCode >= 500
}

// ErrNoJSON means the model did not return a parseable JSON object.
var ErrNoJSON = errors.New("model response contained no JSON object")

// IsRetryable reports whether err is worth retrying on the same provider.
func IsRetryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Retryable()
	}
	var re interface{ Retryable() bool }
	if errors.As(err, &re) {
		return re.Retryable()
	}
	// Network errors and timeouts (but not caller cancellation).
	if errors.Is(err, context.Canceled) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || isNetErr(err)
}
