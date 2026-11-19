package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

func init() {
	Register("bedrock", func(cfg ModelConfig, _ *slog.Logger) (Client, error) { return newBedrock(cfg) })
}

// bedrockClient uses the Bedrock Converse API, which gives one request shape
// for every model family on Bedrock (Anthropic, Amazon Nova, OpenAI, Meta,
// Mistral, DeepSeek, Qwen, Moonshot, Z.AI, ...). Model may be a model ID, an
// inference profile ID (e.g. "us.<model-id>") or an ARN.
//
// Structured output uses a forced tool call. Models that reject forced tool
// use or system prompts are detected on the first call, remembered, and
// retried with the schema (and system prompt) in the messages instead.
type bedrockClient struct {
	cfg ModelConfig
	rt  *bedrockruntime.Client

	noForcedTool atomic.Bool
	noSystem     atomic.Bool
}

func newBedrock(cfg ModelConfig) (*bedrockClient, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AWSProfile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(cfg.AWSProfile))
	}
	// Retries are handled by our wrapper so behaviour matches other providers.
	opts = append(opts, awsconfig.WithRetryMaxAttempts(1))
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	if awsCfg.Region == "" {
		return nil, errors.New("AWS region is not set: use region in the model config or AWS_REGION")
	}
	rt := bedrockruntime.NewFromConfig(awsCfg, func(o *bedrockruntime.Options) {
		if cfg.BaseURL != "" {
			o.BaseEndpoint = aws.String(cfg.BaseURL)
		}
	})
	return &bedrockClient{cfg: cfg, rt: rt}, nil
}

func (c *bedrockClient) Name() string { return "bedrock:" + c.cfg.Model }

func (c *bedrockClient) Complete(ctx context.Context, req Request) (*Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		forced := req.Schema != nil && !c.noForcedTool.Load()
		resp, err := c.converse(ctx, req, forced, c.noSystem.Load())
		if err == nil {
			return resp, nil
		}
		msg := strings.ToLower(validationMessage(err))
		switch {
		case msg == "":
			return nil, err
		case forced && (strings.Contains(msg, "tool") || strings.Contains(msg, "toolchoice")):
			c.noForcedTool.Store(true)
		case !c.noSystem.Load() && req.System != "" && strings.Contains(msg, "system"):
			c.noSystem.Store(true)
		default:
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s: model rejected every request shape", c.Name())
}

func (c *bedrockClient) converse(ctx context.Context, req Request, forced, systemInUser bool) (*Response, error) {
	system := req.System
	if req.Schema != nil && !forced {
		system += schemaInstruction(req.Schema)
	}
	in := &bedrockruntime.ConverseInput{ModelId: aws.String(c.cfg.Model)}
	if system != "" && !systemInUser {
		in.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: system}}
	}
	for i, m := range req.Messages {
		role := types.ConversationRoleUser
		if m.Role == Assistant {
			role = types.ConversationRoleAssistant
		}
		text := m.Content
		if i == 0 && systemInUser && system != "" {
			text = "<instructions>\n" + system + "\n</instructions>\n\n" + text
		}
		in.Messages = append(in.Messages, types.Message{
			Role:    role,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}},
		})
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = c.cfg.MaxTokens
	}
	inf := &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(maxTok))}
	if t := firstTemp(req.Temperature, c.cfg.Temperature); t != nil {
		inf.Temperature = aws.Float32(float32(*t))
	}
	in.InferenceConfig = inf
	if forced {
		name := schemaName(req.Schema)
		in.ToolConfig = &types.ToolConfiguration{
			Tools: []types.Tool{&types.ToolMemberToolSpec{Value: types.ToolSpecification{
				Name:        aws.String(name),
				Description: aws.String(firstNonEmpty(req.Schema.Description, "Return the result.")),
				InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(req.Schema.JSON)},
			}}},
			ToolChoice: &types.ToolChoiceMemberTool{Value: types.SpecificToolChoice{Name: aws.String(name)}},
		}
	}

	cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	out, err := c.rt.Converse(cctx, in)
	if err != nil {
		return nil, wrapAWSErr(err)
	}
	resp := &Response{Model: c.cfg.Model, StopReason: string(out.StopReason)}
	if out.Usage != nil {
		// Some models report prompt-cache hits separately; count all input.
		in := aws.ToInt32(out.Usage.InputTokens) + aws.ToInt32(out.Usage.CacheReadInputTokens) + aws.ToInt32(out.Usage.CacheWriteInputTokens)
		resp.Usage = Usage{InputTokens: int(in), OutputTokens: int(aws.ToInt32(out.Usage.OutputTokens)), CachedInputTokens: int(aws.ToInt32(out.Usage.CacheReadInputTokens))}
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil, fmt.Errorf("bedrock: unexpected output type %T", out.Output)
	}
	var text strings.Builder
	for _, b := range msg.Value.Content {
		switch v := b.(type) {
		case *types.ContentBlockMemberText:
			text.WriteString(v.Value)
		case *types.ContentBlockMemberToolUse:
			if req.Schema != nil && resp.JSON == nil && v.Value.Input != nil {
				// MarshalSmithyDocument re-encodes the original JSON. Decoding
				// into `any` first would turn numbers into strings.
				raw, err := v.Value.Input.MarshalSmithyDocument()
				if err != nil {
					return nil, fmt.Errorf("bedrock: encode tool input: %w", err)
				}
				resp.JSON = json.RawMessage(raw)
			}
		}
	}
	resp.Text = text.String()
	if req.Schema != nil && resp.JSON == nil {
		raw, err := ExtractJSON(resp.Text)
		if err != nil {
			return nil, fmt.Errorf("%s (stopReason=%s): %w", c.Name(), out.StopReason, err)
		}
		resp.JSON = raw
	}
	return resp, nil
}

// validationMessage returns the message of a Bedrock ValidationException,
// or "" for any other error.
func validationMessage(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "ValidationException" {
		return api.ErrorMessage()
	}
	return ""
}

// awsErr marks throttling and server faults as retryable.
type awsErr struct{ error }

func (e awsErr) Unwrap() error { return e.error }

func (e awsErr) Retryable() bool {
	var api smithy.APIError
	if errors.As(e.error, &api) {
		switch api.ErrorCode() {
		case "ThrottlingException", "ServiceUnavailableException", "InternalServerException", "ModelNotReadyException", "ModelTimeoutException":
			return true
		}
		return api.ErrorFault() == smithy.FaultServer
	}
	return isNetErr(e.error)
}

func wrapAWSErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return awsErr{fmt.Errorf("bedrock: %w", err)}
}
