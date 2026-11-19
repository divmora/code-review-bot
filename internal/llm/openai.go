package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// openAICompat describes an OpenAI-compatible service.
type openAICompat struct {
	baseURL     string   // default base URL ("" = must be configured)
	keyEnvs     []string // env vars tried for the API key
	baseEnvs    []string // env vars tried for the base URL
	keyOptional bool
	structured  string // default structured-output mode
	azure       bool   // send "api-key" header instead of bearer
	modern      bool   // uses max_completion_tokens (OpenAI, Azure)
}

var compatProviders = map[string]openAICompat{
	"openai":            {baseURL: "https://api.openai.com/v1", keyEnvs: []string{"OPENAI_API_KEY"}, baseEnvs: []string{"OPENAI_BASE_URL"}, structured: "json_schema", modern: true},
	"azure":             {keyEnvs: []string{"AZURE_OPENAI_API_KEY"}, baseEnvs: []string{"AZURE_OPENAI_BASE_URL"}, structured: "json_schema", azure: true, modern: true},
	"ollama":            {baseURL: "http://localhost:11434/v1", baseEnvs: []string{"OLLAMA_BASE_URL"}, keyOptional: true, structured: "json_object"},
	"openrouter":        {baseURL: "https://openrouter.ai/api/v1", keyEnvs: []string{"OPENROUTER_API_KEY"}, structured: "json_object"},
	"groq":              {baseURL: "https://api.groq.com/openai/v1", keyEnvs: []string{"GROQ_API_KEY"}, structured: "json_object"},
	"deepseek":          {baseURL: "https://api.deepseek.com/v1", keyEnvs: []string{"DEEPSEEK_API_KEY"}, structured: "json_object"},
	"mistral":           {baseURL: "https://api.mistral.ai/v1", keyEnvs: []string{"MISTRAL_API_KEY"}, structured: "json_object"},
	"xai":               {baseURL: "https://api.x.ai/v1", keyEnvs: []string{"XAI_API_KEY"}, structured: "json_object"},
	"together":          {baseURL: "https://api.together.xyz/v1", keyEnvs: []string{"TOGETHER_API_KEY"}, structured: "json_object"},
	"litellm":           {baseURL: "http://localhost:4000/v1", keyEnvs: []string{"LITELLM_API_KEY"}, baseEnvs: []string{"LITELLM_BASE_URL"}, keyOptional: true, structured: "json_object"},
	"openai-compatible": {keyEnvs: []string{"OPENAI_COMPATIBLE_API_KEY"}, baseEnvs: []string{"OPENAI_COMPATIBLE_BASE_URL"}, keyOptional: true, structured: "prompt"},
}

func init() {
	for name, spec := range compatProviders {
		spec := spec
		name := name
		Register(name, func(cfg ModelConfig, logger *slog.Logger) (Client, error) {
			return newOpenAI(name, spec, cfg)
		})
	}
}

type openAIClient struct {
	provider string
	spec     openAICompat
	cfg      ModelConfig
	url      string
	headers  map[string]string
	hc       *http.Client
}

func newOpenAI(name string, spec openAICompat, cfg ModelConfig) (*openAIClient, error) {
	base := cfg.BaseURL
	for _, e := range spec.baseEnvs {
		if base != "" {
			break
		}
		base = os.Getenv(e)
	}
	if base == "" {
		base = spec.baseURL
	}
	if base == "" {
		return nil, fmt.Errorf("base_url is required (or set %s)", strings.Join(spec.baseEnvs, "/"))
	}
	key := resolveKey(cfg, spec.keyEnvs...)
	if key == "" && !spec.keyOptional {
		return nil, fmt.Errorf("API key missing: set %s or api_key/api_key_env", strings.Join(spec.keyEnvs, "/"))
	}
	headers := map[string]string{}
	if key != "" {
		if spec.azure {
			headers["api-key"] = key
		} else {
			headers["Authorization"] = "Bearer " + key
		}
	}
	if cfg.StructuredOutput != "" {
		spec.structured = cfg.StructuredOutput
	}
	switch spec.structured {
	case "json_schema", "json_object", "prompt":
	default:
		return nil, fmt.Errorf("structured_output %q: want json_schema, json_object or prompt", spec.structured)
	}
	return &openAIClient{
		provider: name,
		spec:     spec,
		cfg:      cfg,
		url:      strings.TrimRight(base, "/") + "/chat/completions",
		headers:  mergeHeaders(headers, cfg.Headers),
		hc:       &http.Client{Timeout: cfg.Timeout},
	}, nil
}

func (c *openAIClient) Name() string { return c.provider + ":" + c.cfg.Model }

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *openAIClient) Complete(ctx context.Context, req Request) (*Response, error) {
	system := req.System
	if req.Schema != nil && c.spec.structured != "json_schema" {
		// json_object mode needs the shape spelled out; prompt mode needs
		// the instruction too.
		system += schemaInstruction(req.Schema)
	}
	msgs := []oaMessage{}
	if system != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: system})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, oaMessage{Role: string(m.Role), Content: m.Content})
	}
	body := map[string]any{"model": c.cfg.Model, "messages": msgs}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = c.cfg.MaxTokens
	}
	if c.spec.modern {
		body["max_completion_tokens"] = maxTok
	} else {
		body["max_tokens"] = maxTok
	}
	if t := firstTemp(req.Temperature, c.cfg.Temperature); t != nil {
		body["temperature"] = *t
	}
	if req.Schema != nil {
		switch c.spec.structured {
		case "json_schema":
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   schemaName(req.Schema),
					"schema": req.Schema.JSON,
				},
			}
		case "json_object":
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	}
	var out oaResponse
	if err := httpJSON(ctx, c.hc, c.provider, c.url, c.headers, body, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%s: empty choices", c.provider)
	}
	ch := out.Choices[0]
	if ch.Message.Content == "" && ch.Message.Refusal != "" {
		return nil, fmt.Errorf("%s: model refused: %s", c.provider, ch.Message.Refusal)
	}
	resp := &Response{
		Text:       ch.Message.Content,
		Model:      firstNonEmpty(out.Model, c.cfg.Model),
		StopReason: ch.FinishReason,
		Usage:      Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
	}
	if req.Schema != nil {
		raw, err := ExtractJSON(resp.Text)
		if err != nil {
			return nil, fmt.Errorf("%s (finish_reason=%s): %w", c.Name(), ch.FinishReason, err)
		}
		resp.JSON = raw
	}
	return resp, nil
}

func schemaName(s *Schema) string {
	if s.Name != "" {
		return s.Name
	}
	return "response"
}

func firstTemp(vals ...*float64) *float64 {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
