package gemini

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/interactions"
	"google.golang.org/genai/interactions/models/operations"
	"google.golang.org/genai/interactions/types/stream"
)

type Client struct {
	client *genai.Client
	model  string

	// Gemini 3/4 strictly requires thought steps (with their encrypted
	// signatures) to be replayed before any function_call in the active turn.
	// Keeping them cached here by call ID avoids leaking provider-specific
	// signatures into shared message/event types.
	mu               sync.Mutex
	thoughtsByCallID map[string][]interactions.ThoughtStep
}

func New(log func(string), s *setting.Settings) (*Client, error) {
	if s.GeminiModel == "" {
		return nil, fmt.Errorf("missing gemini model: set --gemini-model or AG_GEMINI_MODEL")
	}
	cc := &genai.ClientConfig{
		APIKey: s.GeminiApiKey,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    s.GeminiBaseURL,
			APIVersion: s.GeminiAPIVersion,
		},
	}
	client, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		return nil, fmt.Errorf("genai.NewClient: %w", err)
	}
	return &Client{
		client:           client,
		model:            s.GeminiModel,
		thoughtsByCallID: make(map[string][]interactions.ThoughtStep),
	}, nil
}

func (c *Client) cacheThoughts(callID string, thoughts []interactions.ThoughtStep) {
	if callID == "" || len(thoughts) == 0 {
		return
	}
	copied := slices.Clone(thoughts)
	c.mu.Lock()
	if c.thoughtsByCallID == nil {
		c.thoughtsByCallID = make(map[string][]interactions.ThoughtStep)
	}
	c.thoughtsByCallID[callID] = copied
	c.mu.Unlock()
}

func (c *Client) lookupThoughts(callID string) []interactions.ThoughtStep {
	if callID == "" {
		return nil
	}
	c.mu.Lock()
	thoughts := c.thoughtsByCallID[callID]
	c.mu.Unlock()
	return thoughts
}

func (c *Client) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	steps := c.toInteractionsSteps(messages)
	geminiTools := toInteractionsTools(tools)

	body := interactions.CreateModelInteraction{
		Model:  interactions.Model(c.model),
		Input:  genai.Ptr(interactions.NewInteractionsInput(steps)),
		Store:  genai.Ptr(false),
		Stream: genai.Ptr(true),
		GenerationConfig: &interactions.GenerationConfig{
			ThinkingSummaries: interactions.ThinkingSummariesAuto.ToPointer(),
		},
	}
	if sysInstruction := joinInstructions(instructions); sysInstruction != "" {
		body.SystemInstruction = genai.Ptr(sysInstruction)
	}
	if len(geminiTools) > 0 {
		body.Tools = geminiTools
	}

	req := operations.CreateInteractionRequest{
		Body: operations.NewCreateInteractionRequestBody(body),
	}

	events := make(chan models.Event, 64)
	go func() {
		defer close(events)

		res, err := c.client.Interactions.Create(ctx, req)
		if err != nil {
			events <- &models.OutputErr{Err: fmt.Sprintf("interaction error: %v", err)}
			return
		}

		if res.InteractionSSEStreamEvent != nil {
			c.consumeStream(res.InteractionSSEStreamEvent, events)
			return
		}
		if res.Interaction != nil {
			c.emitUnaryInteraction(res.Interaction, events)
		}
	}()

	return events
}

func (c *Client) consumeStream(s *stream.EventStream[interactions.InteractionSSEStreamEvent], events chan<- models.Event) {
	defer s.Close()

	activeThoughtIndex := -1
	var activeThoughtSummary strings.Builder
	activeThoughtSig := ""
	var pendingThoughts []interactions.ThoughtStep

	activeFnCallIndex := -1
	activeFnCallHasArgs := false
	sawErr := false

	flushActiveThought := func() {
		if activeThoughtIndex != -1 {
			if activeThoughtSig != "" {
				thought := interactions.ThoughtStep{
					Signature: genai.Ptr(activeThoughtSig),
				}
				if activeThoughtSummary.Len() > 0 {
					thought.Summary = []interactions.ThoughtSummaryContent{
						interactions.NewThoughtSummaryContent(interactions.TextContent{
							Text: activeThoughtSummary.String(),
						}),
					}
				}
				pendingThoughts = append(pendingThoughts, thought)
			}
			activeThoughtIndex = -1
			activeThoughtSummary.Reset()
			activeThoughtSig = ""
		}
	}

	flushActiveFnCall := func() {
		if activeFnCallIndex != -1 {
			if !activeFnCallHasArgs {
				events <- &models.OutputFnArgs{Args: "{}"}
			}
			activeFnCallIndex = -1
			activeFnCallHasArgs = false
		}
	}

	for s.Next() {
		ev := s.Value()
		if ev == nil {
			continue
		}
		data := ev.Data

		if stepStart := data.StepStart; stepStart != nil {
			flushActiveThought()
			flushActiveFnCall()
			if ts := stepStart.GetStepThought(); ts != nil {
				activeThoughtIndex = stepStart.Index
				if summary := thoughtSummaryText(ts); summary != "" {
					activeThoughtSummary.WriteString(summary)
					events <- &models.OutputReasoning{Reasoning: summary}
				}
				if ts.Signature != nil && *ts.Signature != "" {
					activeThoughtSig = *ts.Signature
				}
			}
			if mo := stepStart.GetStepModelOutput(); mo != nil {
				if emitModelOutputStep(mo, events) {
					sawErr = true
				}
			}
			if fc := stepStart.GetStepFunctionCall(); fc != nil {
				if fc.ID != "" && len(pendingThoughts) > 0 {
					c.cacheThoughts(fc.ID, pendingThoughts)
					pendingThoughts = nil
				}
				events <- &models.OutputFnCall{CallId: fc.ID, Name: fc.Name}
				activeFnCallIndex = stepStart.Index
				activeFnCallHasArgs = false
				if len(fc.Arguments) > 0 {
					events <- &models.OutputFnArgs{Args: marshalArgs(fc.Arguments)}
					activeFnCallHasArgs = true
				}
			}
			continue
		}

		if stepDelta := data.StepDelta; stepDelta != nil {
			if td := stepDelta.GetDeltaText(); td != nil && td.Text != "" {
				events <- &models.OutputText{Text: td.Text}
			}
			if ts := stepDelta.GetDeltaThoughtSummary(); ts != nil {
				if tc := ts.GetContentText(); tc != nil && tc.Text != "" {
					if activeThoughtIndex == -1 {
						activeThoughtIndex = stepDelta.Index
					}
					if stepDelta.Index == activeThoughtIndex {
						activeThoughtSummary.WriteString(tc.Text)
					}
					events <- &models.OutputReasoning{Reasoning: tc.Text}
				}
			}
			if sig := stepDelta.GetDeltaThoughtSignature(); sig != nil && sig.Signature != nil && *sig.Signature != "" {
				if activeThoughtIndex == -1 {
					activeThoughtIndex = stepDelta.Index
				}
				if stepDelta.Index == activeThoughtIndex {
					activeThoughtSig = *sig.Signature
				}
			}
			if ad := stepDelta.GetDeltaArgumentsDelta(); ad != nil && ad.Arguments != nil && *ad.Arguments != "" {
				events <- &models.OutputFnArgs{Args: *ad.Arguments}
				if stepDelta.Index == activeFnCallIndex {
					activeFnCallHasArgs = true
				}
			}
			continue
		}

		if stepStop := data.StepStop; stepStop != nil {
			if stepStop.Index == activeThoughtIndex {
				flushActiveThought()
			}
			if stepStop.Index == activeFnCallIndex {
				flushActiveFnCall()
			}
			continue
		}

		if completed := data.InteractionCompletedEvent; completed != nil {
			flushActiveThought()
			flushActiveFnCall()
			if usage := completed.Interaction.Usage; usage != nil {
				events <- toTokenUsage(usage)
			}
			if completed.Interaction.Status == interactions.InteractionSseEventInteractionStatusFailed && !sawErr {
				events <- &models.OutputErr{Err: "interaction failed"}
				sawErr = true
			}
			continue
		}

		if errEv := data.ErrorEvent; errEv != nil {
			flushActiveThought()
			flushActiveFnCall()
			events <- &models.OutputErr{Err: formatInteractionError(errEv.Error)}
			sawErr = true
		}
	}

	if err := s.Err(); err != nil {
		events <- &models.OutputErr{Err: fmt.Sprintf("stream error: %v", err)}
		return
	}
	flushActiveThought()
	flushActiveFnCall()
}

func (c *Client) emitUnaryInteraction(inter *interactions.Interaction, events chan<- models.Event) {
	var pendingThoughts []interactions.ThoughtStep
	sawErr := false
	for _, step := range inter.Steps {
		if ts := step.ThoughtStep; ts != nil {
			if summary := thoughtSummaryText(ts); summary != "" {
				events <- &models.OutputReasoning{Reasoning: summary}
			}
			if ts.Signature != nil && *ts.Signature != "" {
				pendingThoughts = append(pendingThoughts, *ts)
			}
		}
		if step.ModelOutputStep != nil {
			if emitModelOutputStep(step.ModelOutputStep, events) {
				sawErr = true
			}
		}
		if fc := step.FunctionCallStep; fc != nil {
			if fc.ID != "" && len(pendingThoughts) > 0 {
				c.cacheThoughts(fc.ID, pendingThoughts)
				pendingThoughts = nil
			}
			events <- &models.OutputFnCall{CallId: fc.ID, Name: fc.Name}
			events <- &models.OutputFnArgs{Args: marshalArgs(fc.Arguments)}
		}
	}
	for i := range inter.Errors {
		events <- &models.OutputErr{Err: formatInteractionError(&inter.Errors[i])}
		sawErr = true
	}
	if inter.Usage != nil {
		events <- toTokenUsage(inter.Usage)
	}
	if inter.Status == interactions.InteractionStatusFailed && !sawErr {
		events <- &models.OutputErr{Err: "interaction failed"}
	}
}

func thoughtSummaryText(ts *interactions.ThoughtStep) string {
	var summary strings.Builder
	for _, block := range ts.Summary {
		if block.TextContent != nil && block.TextContent.Text != "" {
			summary.WriteString(block.TextContent.Text)
		}
	}
	return summary.String()
}

func emitModelOutputStep(mo *interactions.ModelOutputStep, events chan<- models.Event) (emittedErr bool) {
	for _, block := range mo.Content {
		if block.TextContent != nil && block.TextContent.Text != "" {
			events <- &models.OutputText{Text: block.TextContent.Text}
		}
	}
	if mo.Error != nil {
		events <- &models.OutputErr{Err: formatStatusError(mo.Error)}
		return true
	}
	return false
}

func toTokenUsage(u *interactions.Usage) *models.TokenUsage {
	return &models.TokenUsage{
		InputTokens:           derefInt(u.TotalInputTokens),
		InputTokensCached:     derefInt(u.TotalCachedTokens),
		OutputTokens:          derefInt(u.TotalOutputTokens),
		OutputTokensReasoning: derefInt(u.TotalThoughtTokens),
	}
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func formatInteractionError(e *interactions.Error) string {
	if e == nil {
		return "interaction error"
	}
	code := ""
	if e.Code != nil {
		code = *e.Code
	}
	msg := ""
	if e.Message != nil {
		msg = *e.Message
	}
	switch {
	case code != "" && msg != "":
		return fmt.Sprintf("interaction error (%s): %s", code, msg)
	case msg != "":
		return fmt.Sprintf("interaction error: %s", msg)
	case code != "":
		return fmt.Sprintf("interaction error (%s)", code)
	default:
		return "interaction error"
	}
}

func formatStatusError(s *interactions.Status) string {
	if s == nil {
		return "model output error"
	}
	if s.Message != nil && *s.Message != "" {
		return fmt.Sprintf("model output error: %s", *s.Message)
	}
	if s.Code != nil {
		return fmt.Sprintf("model output error (code %d)", *s.Code)
	}
	return "model output error"
}

func joinInstructions(instructions []string) string {
	parts := make([]string, 0, len(instructions))
	for _, s := range instructions {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

func userInputStep(text string) interactions.Step {
	return interactions.NewStep(interactions.UserInputStep{
		Content: []interactions.Content{
			interactions.NewContent(interactions.TextContent{Text: text}),
		},
	})
}

func modelOutputStep(text string) interactions.Step {
	return interactions.NewStep(interactions.ModelOutputStep{
		Content: []interactions.Content{
			interactions.NewContent(interactions.TextContent{Text: text}),
		},
	})
}

func (c *Client) toInteractionsSteps(messages []models.Message) []interactions.Step {
	steps := make([]interactions.Step, 0, len(messages))
	seenCallIds := make(map[string]struct{})
	for _, msg := range messages {
		switch m := msg.(type) {
		case *models.ContextId:
			panic("ContextId should not be in messages")
		case *models.Summary:
			steps = append(steps, userInputStep(m.Text))
		case *models.UserPrompt:
			steps = append(steps, userInputStep(m.Text))
		case *models.AgentReasoning:
			// Gemini validates thought signatures on replay; signed thought
			// steps are replayed from c.thoughtsByCallID before their tool calls.
			continue
		case *models.AgentResponse:
			steps = append(steps, modelOutputStep(m.Text))
		case *models.OutputErr:
			steps = append(steps, modelOutputStep(m.Err))
		case *models.ToolCalls:
			callItems := make([]*models.ToolCall, 0, len(m.Items))
			for _, tc := range m.Items {
				if tc.CallId != "" {
					if _, seen := seenCallIds[tc.CallId]; seen {
						continue
					}
					seenCallIds[tc.CallId] = struct{}{}
				}
				callItems = append(callItems, tc)
			}
			for _, tc := range callItems {
				for _, thought := range c.lookupThoughts(tc.CallId) {
					steps = append(steps, interactions.NewStep(thought))
				}
				steps = append(steps, interactions.NewStep(interactions.FunctionCallStep{
					ID:        tc.CallId,
					Name:      tc.Name,
					Arguments: parseToolArgs(tc.Args),
				}))
			}
			for _, tc := range callItems {
				steps = append(steps, interactions.NewStep(interactions.FunctionResultStep{
					CallID: tc.CallId,
					Name:   genai.Ptr(tc.Name),
					Result: interactions.NewFunctionResultStepResultUnion([]interactions.FunctionResultSubcontent{
						interactions.NewFunctionResultSubcontent(interactions.TextContent{Text: tc.Result}),
					}),
				}))
			}
		default:
			panic(fmt.Sprintf("unsupported message type: %T", msg))
		}
	}
	return steps
}

func parseToolArgs(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return map[string]any{"raw": raw}
	}
	return args
}

func marshalArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	bs, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(bs)
}

func toInteractionsTools(tools []*tool.Tool) []interactions.Tool {
	if len(tools) == 0 {
		return nil
	}
	geminiTools := make([]interactions.Tool, 0, len(tools))
	for _, t := range tools {
		var params map[string]any
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &params); err != nil {
				panic(fmt.Sprintf("failed to unmarshal tool input schema for tool %s: %v", t.Name, err))
			}
		}
		geminiTools = append(geminiTools, interactions.NewTool(interactions.Function{
			Name:        new(t.Name),
			Description: t.Description,
			Parameters:  params,
		}))
	}
	return geminiTools
}
