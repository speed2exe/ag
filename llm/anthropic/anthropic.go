package anthropic

import (
	"context"
	"fmt"
	"math"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
)

type Client struct {
	an    anthropic.Client
	model string
}

func New(log func(string), setting *setting.Settings) (*Client, error) {
	opts := []option.RequestOption{}
	if setting.AnthropicBaseUrl != "" {
		opts = append(opts, option.WithBaseURL(setting.AnthropicBaseUrl))
	}
	return &Client{
		an:    anthropic.NewClient(opts...),
		model: setting.AnthropicModel,
	}, nil
}

func (c *Client) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	anMessages := make([]anthropic.MessageParam, len(messages))
	for i, msg := range messages {
		anMessages[i] = toAnthropicMessages(ctx, msg)
	}

	anTools, err := toAnthropicTools(tools)
	if err != nil {
		panic(fmt.Errorf("failed to convert tools for Anthropic: %w", err))
	}
	systemPrompt := toSystemPrompt(instructions)

	stream := c.an.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		System:   systemPrompt,
		Messages: anMessages,
		Model:    c.model,
		Tools:    anTools,

		MaxTokens: int64(128 * 1024), // 128k tokens
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfEnabled: &anthropic.ThinkingConfigEnabledParam{
				BudgetTokens: int64(math.MaxInt32),
			},
		},
	})

	events := make(chan models.Event, 64)

	go func() {
		defer close(events)

		for stream.Next() {
			anEvent := stream.Current()
			if anEvent.ContentBlock.Type == "tool_use" {
				events <- &models.OutputFnCall{
					CallId: anEvent.ContentBlock.ID,
					Name:   anEvent.ContentBlock.Name,
				}
				continue
			}
			delta := anEvent.Delta
			if delta.StopReason == anthropic.StopReasonEndTurn {
				events <- &models.TokenUsage{
					InputTokens:           int(anEvent.Usage.InputTokens),
					InputTokensCached:     int(anEvent.Usage.CacheReadInputTokens),
					OutputTokens:          int(anEvent.Usage.OutputTokens),
					OutputTokensReasoning: int(anEvent.Usage.OutputTokensDetails.ThinkingTokens),
				}
			}
			switch delta.Type {
			case "thinking_delta":
				events <- &models.OutputReasoning{Reasoning: delta.Thinking}
			case "text_delta":
				events <- &models.OutputText{Text: delta.Text}
			case "input_json_delta":
				events <- &models.OutputFnArgs{Args: delta.PartialJSON}
			}
		}
		if err := stream.Err(); err != nil {
			events <- &models.OutputErr{Err: fmt.Sprintf("stream error: %v", err)}
		}
	}()

	return events
}

func toAnthropicMessages(ctx context.Context, msg models.Message) anthropic.MessageParam {
	switch m := msg.(type) {
	case *models.ContextId:
		panic("context id message type is not supported by Anthropic API")
	case *models.Summary:
		return textMessage(anthropic.MessageParamRoleUser, m.Text)
	case *models.UserPrompt:
		return textMessage(anthropic.MessageParamRoleUser, m.Text)
	case *models.AgentResponse:
		return textMessage(anthropic.MessageParamRoleAssistant, m.Text)
	case *models.OutputErr:
		return textMessage(anthropic.MessageParamRoleAssistant, m.Err)
	case *models.ToolCalls:
		content := []anthropic.ContentBlockParamUnion{}

		for _, tc := range m.Items {
			content = append(content, anthropic.ContentBlockParamUnion{
				OfToolUse: &anthropic.ToolUseBlockParam{
					ID:     tc.CallId,
					Input:  tc.Args,
					Name:   tc.Name,
					Caller: anthropic.ToolUseBlockParamCallerUnion{OfDirect: &anthropic.DirectCallerParam{}},
				},
			})

			if toolResult, isAnTool, err := callAnTool(ctx, tc.CallId, tc.Name, tc.Args); isAnTool {
				if err != nil {
					content = append(content, toolCallResultErrorText(tc, err))
				} else {
					content = append(content, toolResult)
				}
			} else {
				content = append(content, toolCallResultText(tc))
			}
		}

		return anthropic.MessageParam{
			Content: content,
			Role:    anthropic.MessageParamRoleAssistant,
		}
	default:
		panic(fmt.Sprintf("unsupported message type: %T", msg))
	}
}

func textMessage(role anthropic.MessageParamRole, text string) anthropic.MessageParam {
	return anthropic.MessageParam{
		Content: []anthropic.ContentBlockParamUnion{{OfText: &anthropic.TextBlockParam{Text: text}}},
		Role:    role,
	}
}

func toAnthropicTools(tools []*tool.Tool) ([]anthropic.ToolUnionParam, error) {
	toolParams := make([]anthropic.ToolUnionParam, len(tools))
	for i, tool := range tools {
		var inputSchema anthropic.ToolInputSchemaParam
		if err := inputSchema.UnmarshalJSON(tool.InputSchema); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool input schema for tool %s: %w", tool.Name, err)
		}

		toolParams[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				InputSchema: inputSchema,
				Name:        tool.Name,
				Description: optString(tool.Description),
			},
		}
	}
	return append(toolParams, anApiTools...), nil
}

func toSystemPrompt(instructions []string) []anthropic.TextBlockParam {
	blocks := make([]anthropic.TextBlockParam, 0, len(instructions))
	for _, instruction := range instructions {
		blocks = append(blocks, anthropic.TextBlockParam{Text: instruction})
	}
	return blocks
}

func optString(s *string) param.Opt[string] {
	if s == nil {
		return param.Null[string]()
	}
	return param.NewOpt(*s)
}

func toolCallResultErrorText(tc *models.ToolCall, err error) anthropic.ContentBlockParamUnion {
	return anthropic.ContentBlockParamUnion{
		OfToolResult: &anthropic.ToolResultBlockParam{
			ToolUseID: tc.CallId,
			Content: []anthropic.ToolResultBlockParamContentUnion{
				{OfText: &anthropic.TextBlockParam{Text: fmt.Sprintf("Error calling tool %s: %v", tc.Name, err)}},
			},
		},
	}
}

func toolCallResultText(tc *models.ToolCall) anthropic.ContentBlockParamUnion {
	return anthropic.ContentBlockParamUnion{
		OfToolResult: &anthropic.ToolResultBlockParam{
			ToolUseID: tc.CallId,
			Content: []anthropic.ToolResultBlockParamContentUnion{
				{OfText: &anthropic.TextBlockParam{Text: tc.Result}},
			},
		},
	}
}
