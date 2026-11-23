package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

func init() {
	Register("anthropic", func(cfg ModelConfig, _ *slog.Logger) (Client, error) { return newAnthropic(cfg) })
}

type anthropicClient struct {
	cfg     ModelConfig
	url     string
	headers map[string]string
	hc      *http.Client
}

func newAnthropic(cfg ModelConfig) (*anthropicClient, error) {
	key := resolveKey(cfg, "ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("API key missing: set ANTHROPIC_API_KEY or api_key/api_key_env")
	}
	base := firstNonEmpty(cfg.BaseURL, os.Getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com")
	return &anthropicClient{
		cfg: cfg,
		url: strings.TrimRight(base, "/") + "/v1/messages",
		headers: mergeHeaders(map[string]string{
			"x-api-key":         key,
			"anthropic-version": "2023-06-01",
		}, cfg.Headers),
		hc: &http.Client{Timeout: cfg.Timeout, CheckRedirect: sameHostRedirects},
	}, nil
}

func (c *anthropicClient) Name() string { return "anthropic:" + c.cfg.Model }

type anthropicResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *anthropicClient) Complete(ctx context.Context, req Request) (*Response, error) {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]any{"role": string(m.Role), "content": m.Content})
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = c.cfg.MaxTokens
	}
	body := map[string]any{"model": c.cfg.Model, "max_tokens": maxTok, "messages": msgs}
	if req.System != "" {
		body["system"] = req.System
	}
	if t := firstTemp(req.Temperature, c.cfg.Temperature); t != nil {
		body["temperature"] = *t
	}
	if req.Schema != nil {
		// Forced tool use is Anthropic's structured-output mechanism: the
		// tool input is the JSON object.
		name := schemaName(req.Schema)
		body["tools"] = []map[string]any{{
			"name":         name,
			"description":  firstNonEmpty(req.Schema.Description, "Return the result."),
			"input_schema": req.Schema.JSON,
		}}
		body["tool_choice"] = map[string]any{"type": "tool", "name": name}
	}
	var out anthropicResponse
	if err := httpJSON(ctx, c.hc, "anthropic", c.url, c.headers, body, &out); err != nil {
		return nil, err
	}
	resp := &Response{
		Model:      firstNonEmpty(out.Model, c.cfg.Model),
		StopReason: out.StopReason,
		Usage:      Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
	}
	var text strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			if req.Schema != nil && resp.JSON == nil {
				resp.JSON = b.Input
			}
		}
	}
	resp.Text = text.String()
	if req.Schema != nil && resp.JSON == nil {
		raw, err := ExtractJSON(resp.Text)
		if err != nil {
			return nil, fmt.Errorf("%s (stop_reason=%s): %w", c.Name(), out.StopReason, err)
		}
		resp.JSON = raw
	}
	return resp, nil
}
