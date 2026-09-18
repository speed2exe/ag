package openai

import (
	"context"
	"fmt"
	"strings"

	"github.com/speed2exe/ag/log"
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
			log.Debugf("%s", ev.RawJSON())
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

func toResponsesInput(messages []models.Message) responses.ResponseInputParam {
	var input responses.ResponseInputParam
	for _, msg := range messages {
		input = append(input, convertMessage(msg)...)
	}
	return input
}

func convertMessage(msg models.Message) []responses.ResponseInputItemUnionParam {
	easyMsg := func(role responses.EasyInputMessageRole, text string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{
			OfMessage: &responses.EasyInputMessageParam{
				Role:    role,
				Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(text)},
			},
		}
	}

	switch m := msg.(type) {
	case *models.ContextId:
		return nil
	case *models.Summary:
		return []responses.ResponseInputItemUnionParam{easyMsg(responses.EasyInputMessageRoleUser, m.Text)}
	case *models.UserPrompt:
		return []responses.ResponseInputItemUnionParam{easyMsg(responses.EasyInputMessageRoleUser, m.Text)}
	case *models.AgentResponse:
		return []responses.ResponseInputItemUnionParam{easyMsg(responses.EasyInputMessageRoleAssistant, m.Text)}
	case *models.OutputErr:
		return []responses.ResponseInputItemUnionParam{easyMsg(responses.EasyInputMessageRoleAssistant, m.Err)}
	case *models.ToolCallResult:
		fnCall := responses.ResponseInputItemUnionParam{
			OfFunctionCall: &responses.ResponseFunctionToolCallParam{
				CallID:    m.CallId,
				Name:      m.Name,
				Arguments: m.Args,
			},
		}
		fnOutput := responses.ResponseInputItemUnionParam{
			OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
				CallID: m.CallId,
				Output: m.Result,
			},
		}
		return []responses.ResponseInputItemUnionParam{fnCall, fnOutput}
	default:
		panic(fmt.Sprintf("unsupported message type: %T", msg))
	}
}

func toResponsesTools(tools []*tool.Tool) []responses.ToolUnionParam {
	openAiTools := make([]responses.ToolUnionParam, len(tools))
	for i, t := range tools {
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
		openAiTools[i] = fn
	}
	return openAiTools
}

type ToolCall struct {
	CallId string
	Name   string
	Args   *strings.Builder
}
