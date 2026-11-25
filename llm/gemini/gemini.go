package gemini

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"sync"

	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
)

type Client struct {
	client   *genai.Client
	model    string
	thoughts sync.Map // callID (string) -> []interactions.Step
}

func New(s *setting.Settings) (*Client, error) {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      s.GeminiApiKey,
		HTTPOptions: genai.HTTPOptions{BaseURL: s.GeminiBaseURL},
	})
	if err != nil {
		return nil, err
	}
	return &Client{client: client, model: s.GeminiModel}, nil
}

func (c *Client) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	events := make(chan models.Event, 64)
	go func() {
		defer close(events)

		res, err := c.client.Interactions.Create(ctx, operations.CreateInteractionRequest{
			Body: operations.NewCreateInteractionRequestBody(interactions.CreateModelInteraction{
				Model:             interactions.Model(c.model),
				SystemInstruction: util.OptPtr(strings.Join(instructions, "\n\n")),
				Input:             new(interactions.NewInteractionsInput(c.toSteps(messages))),
				Tools:             toTools(tools),
				Stream:            new(true),
				GenerationConfig: &interactions.GenerationConfig{
					ThinkingSummaries: new(interactions.ThinkingSummariesAuto),
				},
			}),
		})
		if err != nil {
			events <- &models.OutputErr{Err: fmt.Sprintf("interaction error: %v", err)}
			return
		}
		s := res.InteractionSSEStreamEvent
		if s == nil {
			return
		}
		defer s.Close()

		var pendingThoughts []interactions.Step
		for s.Next() {
			ev := s.Value()
			d := ev.GetDataStepDelta()
			if fc := ev.GetDataStepStart().GetStepFunctionCall(); fc != nil {
				if len(pendingThoughts) > 0 {
					c.thoughts.Store(fc.ID, pendingThoughts)
					pendingThoughts = nil
				}
				events <- &models.OutputFnCall{CallId: fc.ID, Name: fc.Name}
				if len(fc.Arguments) > 0 {
					bs, _ := json.Marshal(fc.Arguments)
					events <- &models.OutputFnArgs{Args: string(bs)}
				}
			}
			if td := d.GetDeltaText(); td != nil {
				events <- &models.OutputText{Text: td.Text}
			}
			if tc := d.GetDeltaThoughtSummary().GetContentText(); tc != nil {
				events <- &models.OutputReasoning{Reasoning: tc.Text}
			}
			if sig := deref(d.GetDeltaThoughtSignature().GetSignature()); sig != "" {
				pendingThoughts = append(pendingThoughts, interactions.NewStep(interactions.ThoughtStep{Signature: new(sig)}))
			}
			if args := deref(d.GetDeltaArgumentsDelta().GetArguments()); args != "" {
				events <- &models.OutputFnArgs{Args: args}
			}
			if u := ev.GetDataInteractionCompleted().GetInteraction().Usage; u != nil {
				events <- &models.TokenUsage{
					InputTokens:           deref(u.TotalInputTokens),
					InputTokensCached:     deref(u.TotalCachedTokens),
					OutputTokens:          deref(u.TotalOutputTokens),
					OutputTokensReasoning: deref(u.TotalThoughtTokens),
				}
			}
			if errEv := ev.GetDataError(); errEv != nil {
				events <- &models.OutputErr{Err: "interaction error: " + deref(errEv.GetError().GetMessage())}
			}
		}

		if err := s.Err(); err != nil {
			events <- &models.OutputErr{Err: fmt.Sprintf("stream error: %v", err)}
		}
	}()

	return events
}

func textContent(text string) []interactions.Content {
	return []interactions.Content{interactions.NewContent(interactions.TextContent{Text: text})}
}

func (c *Client) toSteps(messages []models.Message) (steps []interactions.Step) {
	for _, msg := range messages {
		switch m := msg.(type) {
		case *models.Summary:
			steps = append(steps, interactions.NewStep(interactions.UserInputStep{Content: textContent(m.Text)}))
		case *models.UserPrompt:
			steps = append(steps, interactions.NewStep(interactions.UserInputStep{Content: textContent(m.Text)}))
		case *models.AgentReasoning:
		case *models.AgentResponse:
			steps = append(steps, interactions.NewStep(interactions.ModelOutputStep{Content: textContent(m.Text)}))
		case *models.OutputErr:
			steps = append(steps, interactions.NewStep(interactions.ModelOutputStep{Content: textContent(m.Err)}))
		case *models.ToolCalls:
			for _, tc := range m.Items {
				if t, ok := c.thoughts.Load(tc.CallId); ok {
					steps = append(steps, t.([]interactions.Step)...)
				}
				args := map[string]any{}
				_ = json.Unmarshal([]byte(tc.Args), &args)
				steps = append(steps, interactions.NewStep(interactions.FunctionCallStep{
					ID:        tc.CallId,
					Name:      tc.Name,
					Arguments: args,
				}))
			}
			for _, tc := range m.Items {
				steps = append(steps, interactions.NewStep(interactions.FunctionResultStep{
					CallID: tc.CallId,
					Name:   new(tc.Name),
					Result: interactions.NewFunctionResultStepResultUnion(tc.Result),
				}))
			}
		default:
			panic(fmt.Sprintf("unsupported message type: %T", msg))
		}
	}
	return steps
}

func toTools(tools []*tool.Tool) (res []interactions.Tool) {
	for _, t := range tools {
		var params any
		if err := json.Unmarshal(t.InputSchema, &params); err != nil {
			panic(fmt.Sprintf("failed to unmarshal tool input schema for tool %s: %v", t.Name, err))
		}
		res = append(res, interactions.NewTool(interactions.Function{
			Name:        new(t.Name),
			Description: t.Description,
			Parameters:  params,
		}))
	}
	return res
}

func deref[T any](p *T) (v T) {
	if p != nil {
		v = *p
	}
	return v
}
