package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var testSchema = &Schema{Name: "result", JSON: map[string]any{
	"type":       "object",
	"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	"required":   []string{"ok"},
}}

func TestParseSpec(t *testing.T) {
	cases := map[string]ModelConfig{
		"openai:gpt-x":                    {Provider: "openai", Model: "gpt-x"},
		"bedrock:anthropic.claude-x-v1:0": {Provider: "bedrock", Model: "anthropic.claude-x-v1:0"},
		"Ollama:qwen2.5-coder:14b":        {Provider: "ollama", Model: "qwen2.5-coder:14b"},
		" gemini:gemini-pro ":             {Provider: "gemini", Model: "gemini-pro"},
	}
	for in, want := range cases {
		got, err := ParseSpec(in)
		if err != nil || got.Provider != want.Provider || got.Model != want.Model {
			t.Errorf("ParseSpec(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "gpt-x", "openai:", ":m"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("ParseSpec(%q) should fail", bad)
		}
	}
}

func TestProvidersRegistered(t *testing.T) {
	have := strings.Join(Providers(), ",")
	for _, p := range []string{"openai", "anthropic", "gemini", "bedrock", "azure", "ollama", "openrouter", "litellm", "openai-compatible"} {
		if !strings.Contains(","+have+",", ","+p+",") {
			t.Errorf("provider %s not registered (have %s)", p, have)
		}
	}
}

func TestExtractJSON(t *testing.T) {
	for _, in := range []string{
		`{"ok":true}`,
		"Here you go:\n```json\n{\"ok\": true}\n```\nthanks",
		`prefix {not json} then {"ok":true} trailing`,
	} {
		raw, err := ExtractJSON(in)
		if err != nil {
			t.Errorf("ExtractJSON(%q): %v", in, err)
			continue
		}
		var v struct{ OK bool }
		if json.Unmarshal(raw, &v); !v.OK {
			t.Errorf("ExtractJSON(%q) = %s", in, raw)
		}
	}
	if _, err := ExtractJSON("no json here"); !errors.Is(err, ErrNoJSON) {
		t.Errorf("want ErrNoJSON, got %v", err)
	}
}

func capture(t *testing.T, status int, reply string, check func(r *http.Request, body map[string]any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if check != nil {
			check(r, body)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAIStructured(t *testing.T) {
	srv := capture(t, 200, `{"model":"gpt-x","choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`,
		func(r *http.Request, body map[string]any) {
			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Errorf("auth = %q", got)
			}
			rf, _ := body["response_format"].(map[string]any)
			if rf["type"] != "json_schema" {
				t.Errorf("response_format = %v", body["response_format"])
			}
			if _, ok := body["max_completion_tokens"]; !ok {
				t.Errorf("openai should send max_completion_tokens: %v", body)
			}
			if _, ok := body["temperature"]; ok {
				t.Errorf("temperature must be omitted unless configured")
			}
		})
	c, err := New(ModelConfig{Provider: "openai", Model: "gpt-x", APIKey: "sk-test", BaseURL: srv.URL + "/v1"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), Request{System: "s", Messages: []Message{{User, "hi"}}, Schema: testSchema})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"ok":true}` || resp.Usage.InputTokens != 10 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestOllamaJSONObjectNoKey(t *testing.T) {
	srv := capture(t, 200, `{"choices":[{"message":{"content":"`+"```json\\n{\\\"ok\\\":true}\\n```"+`"}}]}`,
		func(r *http.Request, body map[string]any) {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("ollama without key should not send auth")
			}
			msgs := body["messages"].([]any)
			sys := msgs[0].(map[string]any)["content"].(string)
			if !strings.Contains(sys, "JSON Schema") {
				t.Errorf("json_object mode should describe the schema in the system prompt")
			}
			if _, ok := body["max_tokens"]; !ok {
				t.Errorf("compatible servers get max_tokens")
			}
		})
	c, err := New(ModelConfig{Provider: "ollama", Model: "qwen:7b", BaseURL: srv.URL}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), Request{System: "s", Messages: []Message{{User, "hi"}}, Schema: testSchema})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"ok":true}` {
		t.Errorf("JSON = %s", resp.JSON)
	}
}

func TestAnthropicToolUse(t *testing.T) {
	srv := capture(t, 200, `{"model":"claude-x","stop_reason":"tool_use","content":[{"type":"tool_use","name":"result","input":{"ok":true}}],"usage":{"input_tokens":5,"output_tokens":2}}`,
		func(r *http.Request, body map[string]any) {
			if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
				t.Errorf("headers = %v", r.Header)
			}
			tc := body["tool_choice"].(map[string]any)
			if tc["type"] != "tool" || tc["name"] != "result" {
				t.Errorf("tool_choice = %v", tc)
			}
			if body["system"] != "sys" {
				t.Errorf("system = %v", body["system"])
			}
		})
	c, err := New(ModelConfig{Provider: "anthropic", Model: "claude-x", APIKey: "k", BaseURL: srv.URL}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), Request{System: "sys", Messages: []Message{{User, "hi"}}, Schema: testSchema})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"ok":true}` || resp.StopReason != "tool_use" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestGeminiSchema(t *testing.T) {
	srv := capture(t, 200, `{"candidates":[{"content":{"parts":[{"text":"thinking","thought":true},{"text":"{\"ok\":true}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1}}`,
		func(r *http.Request, body map[string]any) {
			if !strings.HasSuffix(r.URL.Path, "/models/gemini-x:generateContent") {
				t.Errorf("path = %s", r.URL.Path)
			}
			if r.Header.Get("x-goog-api-key") != "g" {
				t.Errorf("missing api key header")
			}
			gen := body["generationConfig"].(map[string]any)
			if gen["responseMimeType"] != "application/json" || gen["responseJsonSchema"] == nil {
				t.Errorf("generationConfig = %v", gen)
			}
		})
	c, err := New(ModelConfig{Provider: "gemini", Model: "gemini-x", APIKey: "g", BaseURL: srv.URL}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), Request{Messages: []Message{{User, "hi"}}, Schema: testSchema})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"ok":true}` || resp.Text != `{"ok":true}` {
		t.Errorf("thought parts must be dropped; resp = %+v", resp)
	}
}

type fakeClient struct {
	name  string
	errs  []error
	calls atomic.Int32
}

func (f *fakeClient) Name() string { return f.name }
func (f *fakeClient) Complete(ctx context.Context, req Request) (*Response, error) {
	n := int(f.calls.Add(1)) - 1
	if n < len(f.errs) && f.errs[n] != nil {
		return nil, f.errs[n]
	}
	return &Response{Text: f.name}, nil
}

func TestRetryThenSuccess(t *testing.T) {
	f := &fakeClient{name: "a", errs: []error{&APIError{StatusCode: 503}}}
	c := WithRetry(f, 2, quiet)
	resp, err := c.Complete(context.Background(), Request{})
	if err != nil || resp.Text != "a" || f.calls.Load() != 2 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, f.calls.Load())
	}
}

func TestNoRetryOn400(t *testing.T) {
	f := &fakeClient{name: "a", errs: []error{&APIError{StatusCode: 400}}}
	if _, err := WithRetry(f, 3, quiet).Complete(context.Background(), Request{}); err == nil || f.calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls.Load())
	}
}

func TestFallback(t *testing.T) {
	a := &fakeClient{name: "a", errs: []error{&APIError{StatusCode: 401}}}
	b := &fakeClient{name: "b"}
	fb := &fallback{clients: []Client{a, b}, logger: quiet}
	resp, err := fb.Complete(context.Background(), Request{})
	if err != nil || resp.Text != "b" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if fb.Name() != "a -> b" {
		t.Errorf("name = %s", fb.Name())
	}
}

func TestMissingKeyError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := New(ModelConfig{Provider: "openai", Model: "m"}, quiet)
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("want helpful key error, got %v", err)
	}
	if _, err := New(ModelConfig{Provider: "nope", Model: "m"}, quiet); err == nil || !strings.Contains(err.Error(), "available") {
		t.Fatalf("want unknown provider error, got %v", err)
	}
}

// fakeBedrock serves the Converse REST API. It rejects forced tool use
// and system prompts when told to, like some Bedrock model families.
func fakeBedrock(t *testing.T, rejectTool, rejectSystem bool, calls *[]map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/converse") || r.Header.Get("Authorization") == "" {
			t.Errorf("unexpected request %s (auth=%q)", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*calls = append(*calls, body)
		fail := func(msg string) {
			w.Header().Set("X-Amzn-ErrorType", "ValidationException")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"message":"`+msg+`"}`)
		}
		if _, ok := body["toolConfig"]; ok && rejectTool {
			fail("This model doesn't support the toolConfig.toolChoice.tool field.")
			return
		}
		if _, ok := body["system"]; ok && rejectSystem {
			fail("This model doesn't support system messages.")
			return
		}
		content := `[{"text":"{\"ok\": true}"}]`
		if _, ok := body["toolConfig"]; ok {
			content = `[{"toolUse":{"toolUseId":"t1","name":"result","input":{"ok":true,"n":0.9,"line":42}}}]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":`+content+`}},"stopReason":"end_turn","usage":{"inputTokens":9,"outputTokens":2,"totalTokens":11},"metrics":{"latencyMs":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBedrockForcedToolAndFallbacks(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	cases := []struct {
		name                     string
		rejectTool, rejectSystem bool
		wantCalls                int
	}{
		{"forced tool works", false, false, 1},
		{"tool rejected", true, false, 2},
		{"tool and system rejected", true, true, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []map[string]any
			srv := fakeBedrock(t, tc.rejectTool, tc.rejectSystem, &calls)
			c, err := New(ModelConfig{Provider: "bedrock", Model: "vendor.model-x", Region: "us-east-1", BaseURL: srv.URL, MaxRetries: -1}, quiet)
			if err != nil {
				t.Fatal(err)
			}
			req := Request{System: "be precise", Messages: []Message{{User, "hi"}}, Schema: testSchema}
			resp, err := c.Complete(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			// Numbers must stay numbers (the SDK document type stores them as strings).
			var got struct {
				OK   bool
				N    *float64
				Line *int
			}
			if err := json.Unmarshal(resp.JSON, &got); err != nil || !got.OK || resp.Usage.InputTokens != 9 {
				t.Errorf("resp = %+v json=%s err=%v", resp, resp.JSON, err)
			}
			if !tc.rejectTool && (got.N == nil || *got.N != 0.9 || got.Line == nil || *got.Line != 42) {
				t.Errorf("tool input numbers mangled: %s", resp.JSON)
			}
			if len(calls) != tc.wantCalls {
				t.Errorf("calls = %d, want %d", len(calls), tc.wantCalls)
			}
			// The learned mode is remembered: a second call needs one request.
			before := len(calls)
			if _, err := c.Complete(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if len(calls)-before != 1 {
				t.Errorf("fallback mode not remembered: %d extra calls", len(calls)-before)
			}
			if tc.rejectSystem {
				first := calls[len(calls)-1]["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
				if !strings.Contains(first, "be precise") || !strings.Contains(first, "JSON Schema") {
					t.Errorf("system prompt and schema should move into the user message: %q", first)
				}
			}
		})
	}
}
