package openai

import (
	"context"
	"fmt"
	"strings"

	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"

	"encoding/json/v2"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
)

type Client struct {
	client openai.Client
	model  string
}

func New(log func(string), s *setting.Settings) (*Client, error) {
	opts := []option.RequestOption{}
	if s.OpenAIApiKey != "" {
		opts = append(opts, option.WithAPIKey(s.OpenAIApiKey))
	}
	if s.OpenAIBaseURL != "" {
		opts = append(opts, option.WithBaseURL(s.OpenAIBaseURL))
	}
	return &Client{
		client: openai.NewClient(opts...),
		model:  s.OpenAIModel,
	}, nil
}

func (c *Client) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	// TODO: add reasoning
	input := toResponsesInput(messages)
	openAiTools := toResponsesTools(tools)

	params := responses.ResponseNewParams{
		Model:        openai.ResponsesModel(c.model),
		Instructions: param.NewOpt(strings.Join(instructions, "\n\n")),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Tools: openAiTools,
	}

	stream := c.client.Responses.NewStreaming(ctx, params)

	events := make(chan models.Event, 64)
	go func() {
		defer close(events)
		defer stream.Close()

		for stream.Next() {
			ev := stream.Current()
			switch ev.Type {
			case "response.output_text.delta":
				events <- &models.OutputText{Text: ev.Delta.OfString}
			case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
				events <- &models.OutputReasoning{Reasoning: ev.Delta.OfString}
			case "response.output_item.added":
				if ev.Item.Type == "function_call" {
					events <- &models.OutputFnCall{CallId: ev.Item.CallID, Name: ev.Item.Name}
				}
			case "response.function_call_arguments.delta":
				events <- &models.OutputFnArgs{Args: ev.Delta.OfString}
			case "response.completed":
				usage := ev.Response.Usage
				events <- &models.TokenUsage{
					InputTokens:           int(usage.InputTokens),
					InputTokensCached:     int(usage.InputTokensDetails.CachedTokens),
					OutputTokens:          int(usage.OutputTokens),
					OutputTokensReasoning: int(usage.OutputTokensDetails.ReasoningTokens),
				}
			case "error":
				e := ev.AsError()
				events <- &models.OutputErr{Err: fmt.Sprintf("response error: %s", e.Message)}
			}
		}

		if err := stream.Err(); err != nil {
			events <- &models.OutputErr{Err: fmt.Sprintf("stream error: %v", err)}
		}
	}()

	return events
}

func easyMsgString(role responses.EasyInputMessageRole, text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{
		OfMessage: &responses.EasyInputMessageParam{
			Role:    role,
			Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(text)},
		},
	}
}

func toResponsesInput(messages []models.Message) responses.ResponseInputParam {
	var input responses.ResponseInputParam
	for _, msg := range messages {
		switch m := msg.(type) {
		case *models.ContextId:
			panic("ContextId should not be in messages")
		case *models.Summary:
			input = append(input, easyMsgString(responses.EasyInputMessageRoleUser, m.Text))
		case *models.UserPrompt:
			input = append(input, easyMsgString(responses.EasyInputMessageRoleUser, m.Text))
		case *models.AgentReasoning:
			input = append(input, responses.ResponseInputItemUnionParam{
				OfReasoning: &responses.ResponseReasoningItemParam{
					Summary: []responses.ResponseReasoningItemSummaryParam{{Text: m.Reasoning}},
				},
			})
		case *models.AgentResponse:
			input = append(input, easyMsgString(responses.EasyInputMessageRoleAssistant, m.Text))
		case *models.OutputErr:
			input = append(input, easyMsgString(responses.EasyInputMessageRoleAssistant, m.Err))
		case *models.ToolCalls:
			for _, tc := range m.Items {
				input = append(input,
					responses.ResponseInputItemUnionParam{
						OfFunctionCall: &responses.ResponseFunctionToolCallParam{
							CallID:    tc.CallId,
							Name:      tc.Name,
							Arguments: tc.Args,
						},
					},
				)
			}
			for _, tc := range m.Items {
				input = append(input,
					responses.ResponseInputItemUnionParam{
						OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
							CallID: tc.CallId,
							Output: tc.Result,
						},
					},
				)
			}
		default:
			panic(fmt.Sprintf("unsupported message type: %T", msg))
		}
	}
	return input
}

func toResponsesTools(tools []*tool.Tool) []responses.ToolUnionParam {
	openAiTools := make([]responses.ToolUnionParam, 0, len(tools)+len(oaiApiTools))
	for _, t := range oaiApiTools {
		openAiTools = append(openAiTools, t)
	}
	for _, t := range tools {
		var params map[string]any
		if err := json.Unmarshal(t.InputSchema, &params); err != nil {
			panic(fmt.Sprintf("failed to unmarshal tool input schema for tool %s: %v", t.Name, err))
		}

		fn := responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:       t.Name,
				Parameters: params,
			},
		}
		if t.Description != nil {
			fn.OfFunction.Description = param.NewOpt(*t.Description)
		}
		openAiTools = append(openAiTools, fn)
	}
	return openAiTools
}

type ToolCall struct {
	CallId string
	Name   string
	Args   *strings.Builder
}
