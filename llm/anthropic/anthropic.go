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

func New(s *setting.Settings) *Client {
	var opts []option.RequestOption
	if s.AnthropicBaseUrl != "" {
		opts = append(opts, option.WithBaseURL(s.AnthropicBaseUrl))
	}
	return &Client{
		an:    anthropic.NewClient(opts...),
		model: s.AnthropicModel,
	}
}

func (c *Client) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	msgs []models.Message,
) <-chan models.Event {
	stream := c.an.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		System:   toSystemPrompt(instructions),
		Messages: toAnthropicMessages(msgs),
		Model:    c.model,
		Tools:    toAnthropicTools(tools),

		MaxTokens: int64(128 * 1024), // 128k tokens, based on https://platform.claude.com/docs/en/models/overview
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

func toAnthropicMessages(msgs []models.Message) []anthropic.MessageParam {
	res := make([]anthropic.MessageParam, 0, len(msgs))
	for _, msg := range msgs {
		switch m := msg.(type) {
		case *models.ContextId:
			panic("context id message type is not supported by Anthropic API")
		case *models.Summary:
			res = append(res, textMessage(anthropic.MessageParamRoleUser, m.Text))
		case *models.UserPrompt:
			res = append(res, textMessage(anthropic.MessageParamRoleUser, m.Text))
		case *models.AgentReasoning:
		case *models.AgentResponse:
			res = append(res, textMessage(anthropic.MessageParamRoleAssistant, m.Text))
		case *models.OutputErr:
			res = append(res, textMessage(anthropic.MessageParamRoleAssistant, m.Err))
		case *models.ToolCalls:
			for _, tc := range m.Items {
				res = append(res, toolCallMessageParam(tc)...)
			}
		default:
			panic(fmt.Sprintf("unsupported message type: %T", msg))
		}
	}
	return res
}

func toolCallMessageParam(tc *models.ToolCall) []anthropic.MessageParam {
	return []anthropic.MessageParam{
		{
			Content: []anthropic.ContentBlockParamUnion{{
				OfToolUse: &anthropic.ToolUseBlockParam{
					ID:     tc.CallId,
					Input:  tc.Args,
					Name:   tc.Name,
					Caller: anthropic.ToolUseBlockParamCallerUnion{OfDirect: &anthropic.DirectCallerParam{}},
				},
			}},
			Role: anthropic.MessageParamRoleAssistant,
		},
		{
			Content: []anthropic.ContentBlockParamUnion{{
				OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: tc.CallId,
					Content: []anthropic.ToolResultBlockParamContentUnion{
						{OfText: &anthropic.TextBlockParam{Text: tc.Result}},
					},
				},
			}},
			Role: anthropic.MessageParamRoleAssistant,
		},
	}
}

func textMessage(role anthropic.MessageParamRole, text string) anthropic.MessageParam {
	return anthropic.MessageParam{
		Content: []anthropic.ContentBlockParamUnion{{OfText: &anthropic.TextBlockParam{Text: text}}},
		Role:    role,
	}
}

func toAnthropicTools(tools []*tool.Tool) []anthropic.ToolUnionParam {
	toolParams := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		var inputSchema anthropic.ToolInputSchemaParam
		if err := inputSchema.UnmarshalJSON(t.InputSchema); err != nil {
			panic(fmt.Errorf("failed to unmarshal tool input schema for tool %s: %w", t.Name, err))
		}
		desc := param.Null[string]()
		if t.Description != nil {
			desc = param.NewOpt(*t.Description)
		}
		toolParams[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				InputSchema: inputSchema,
				Name:        t.Name,
				Description: desc,
			},
		}
	}
	return toolParams
}

func toSystemPrompt(instructions []string) []anthropic.TextBlockParam {
	blocks := make([]anthropic.TextBlockParam, len(instructions))
	for i, instruction := range instructions {
		blocks[i] = anthropic.TextBlockParam{Text: instruction}
	}
	return blocks
}
