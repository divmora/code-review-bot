package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func init() {
	Register("gemini", func(cfg ModelConfig, _ *slog.Logger) (Client, error) { return newGemini(cfg) })
}

// geminiClient talks to the Gemini API (generativelanguage.googleapis.com).
type geminiClient struct {
	cfg     ModelConfig
	url     string
	headers map[string]string
	hc      *http.Client
}

func newGemini(cfg ModelConfig) (*geminiClient, error) {
	key := resolveKey(cfg, "GEMINI_API_KEY", "GOOGLE_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("API key missing: set GEMINI_API_KEY (or GOOGLE_API_KEY) or api_key/api_key_env")
	}
	base := firstNonEmpty(cfg.BaseURL, os.Getenv("GEMINI_BASE_URL"), "https://generativelanguage.googleapis.com/v1beta")
	model := strings.TrimPrefix(cfg.Model, "models/")
	return &geminiClient{
		cfg:     cfg,
		url:     strings.TrimRight(base, "/") + "/models/" + url.PathEscape(model) + ":generateContent",
		headers: mergeHeaders(map[string]string{"x-goog-api-key": key}, cfg.Headers),
		hc:      &http.Client{Timeout: cfg.Timeout},
	}, nil
}

func (c *geminiClient) Name() string { return "gemini:" + c.cfg.Model }

type geminiPart struct {
	Text    string `json:"text,omitempty"`
	Thought bool   `json:"thought,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

func (c *geminiClient) Complete(ctx context.Context, req Request) (*Response, error) {
	contents := make([]geminiContent, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := "user"
		if m.Role == Assistant {
			role = "model"
		}
		contents = append(contents, geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}})
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = c.cfg.MaxTokens
	}
	gen := map[string]any{"maxOutputTokens": maxTok}
	if t := firstTemp(req.Temperature, c.cfg.Temperature); t != nil {
		gen["temperature"] = *t
	}
	if req.Schema != nil {
		gen["responseMimeType"] = "application/json"
		gen["responseJsonSchema"] = req.Schema.JSON
	}
	body := map[string]any{"contents": contents, "generationConfig": gen}
	if req.System != "" {
		body["systemInstruction"] = geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	var out geminiResponse
	if err := httpJSON(ctx, c.hc, "gemini", c.url, c.headers, body, &out); err != nil {
		return nil, err
	}
	if len(out.Candidates) == 0 {
		if out.PromptFeedback.BlockReason != "" {
			return nil, fmt.Errorf("gemini: prompt blocked: %s", out.PromptFeedback.BlockReason)
		}
		return nil, fmt.Errorf("gemini: no candidates returned")
	}
	cand := out.Candidates[0]
	var text strings.Builder
	for _, p := range cand.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	resp := &Response{
		Text:       text.String(),
		Model:      firstNonEmpty(out.ModelVersion, c.cfg.Model),
		StopReason: cand.FinishReason,
		Usage:      Usage{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount},
	}
	if req.Schema != nil {
		raw, err := ExtractJSON(resp.Text)
		if err != nil {
			return nil, fmt.Errorf("%s (finishReason=%s): %w", c.Name(), cand.FinishReason, err)
		}
		resp.JSON = raw
	}
	return resp, nil
}
