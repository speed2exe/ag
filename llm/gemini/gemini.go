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
	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
)

type Client struct {
	client *genai.Client
	model  string

	// Gemini 3/4 requires signed thought steps to be replayed before their
	// corresponding function_call steps.
	mu          sync.Mutex
	thoughtSigs map[string][]string
}

func New(log func(string), s *setting.Settings) (*Client, error) {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      s.GeminiApiKey,
		HTTPOptions: genai.HTTPOptions{BaseURL: s.GeminiBaseURL},
	})
	if err != nil {
		return nil, fmt.Errorf("genai.NewClient: %w", err)
	}
	return &Client{
		client:      client,
		model:       s.GeminiModel,
		thoughtSigs: make(map[string][]string),
	}, nil
}

func (c *Client) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	body := interactions.CreateModelInteraction{
		Model:  interactions.Model(c.model),
		Input:  new(interactions.NewInteractionsInput(c.toInteractionsSteps(messages))),
		Tools:  toInteractionsTools(tools),
		Stream: new(true),
		GenerationConfig: &interactions.GenerationConfig{
			ThinkingSummaries: new(interactions.ThinkingSummariesAuto),
		},
	}
	if sys := strings.Join(instructions, "\n\n"); sys != "" {
		body.SystemInstruction = new(sys)
	}

	events := make(chan models.Event, 64)
	go func() {
		defer close(events)

		res, err := c.client.Interactions.Create(ctx, operations.CreateInteractionRequest{
			Body: operations.NewCreateInteractionRequestBody(body),
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

		var pendingSigs []string
		for s.Next() {
			data := s.Value().Data
			switch {
			case data.StepStart != nil:
				if fc := data.StepStart.Step.FunctionCallStep; fc != nil {
					if fc.ID != "" && len(pendingSigs) > 0 {
						c.mu.Lock()
						c.thoughtSigs[fc.ID] = pendingSigs
						c.mu.Unlock()
						pendingSigs = nil
					}
					events <- &models.OutputFnCall{CallId: fc.ID, Name: fc.Name}
					if len(fc.Arguments) > 0 {
						if bs, err := json.Marshal(fc.Arguments); err == nil {
							events <- &models.OutputFnArgs{Args: string(bs)}
						}
					}
				}
			case data.StepDelta != nil:
				d := data.StepDelta.Delta
				switch {
				case d.TextDelta != nil:
					events <- &models.OutputText{Text: d.TextDelta.Text}
				case d.ThoughtSummaryDelta != nil:
					if tc := d.ThoughtSummaryDelta.GetContentText(); tc != nil {
						events <- &models.OutputReasoning{Reasoning: tc.Text}
					}
				case d.ThoughtSignatureDelta != nil:
					if sig := deref(d.ThoughtSignatureDelta.Signature); sig != "" {
						pendingSigs = append(pendingSigs, sig)
					}
				case d.ArgumentsDelta != nil:
					if args := deref(d.ArgumentsDelta.Arguments); args != "" {
						events <- &models.OutputFnArgs{Args: args}
					}
				}
			case data.InteractionCompletedEvent != nil:
				if u := data.InteractionCompletedEvent.Interaction.Usage; u != nil {
					events <- &models.TokenUsage{
						InputTokens:           deref(u.TotalInputTokens),
						InputTokensCached:     deref(u.TotalCachedTokens),
						OutputTokens:          deref(u.TotalOutputTokens),
						OutputTokensReasoning: deref(u.TotalThoughtTokens),
					}
				}
			case data.ErrorEvent != nil:
				events <- &models.OutputErr{Err: fmt.Sprintf("interaction error: %s", deref(data.ErrorEvent.Error.GetMessage()))}
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

func (c *Client) toInteractionsSteps(messages []models.Message) []interactions.Step {
	steps := make([]interactions.Step, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case *models.ContextId:
			panic("ContextId should not be in messages")
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
				c.mu.Lock()
				sigs := c.thoughtSigs[tc.CallId]
				c.mu.Unlock()
				for _, sig := range sigs {
					steps = append(steps, interactions.NewStep(interactions.ThoughtStep{Signature: new(sig)}))
				}
				var args map[string]any
				_ = json.Unmarshal([]byte(tc.Args), &args)
				if args == nil {
					args = map[string]any{}
				}
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

func toInteractionsTools(tools []*tool.Tool) []interactions.Tool {
	var res []interactions.Tool
	for _, t := range tools {
		var params map[string]any
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &params); err != nil {
				panic(fmt.Sprintf("failed to unmarshal tool input schema for tool %s: %v", t.Name, err))
			}
		}
		res = append(res, interactions.NewTool(interactions.Function{
			Name:        new(t.Name),
			Description: t.Description,
			Parameters:  params,
		}))
	}
	return res
}

func deref[T any](p *T) (zero T) {
	if p == nil {
		return zero
	}
	return *p
}
