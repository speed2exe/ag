package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/state"
	"github.com/speed2exe/ag/store"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func GenerateStream(ctx context.Context,
	model llm.Model,
	tools []*tool.Tool,
	instructions []string,
	ctxId string,
	prompt string,
) (string, <-chan models.Event) {
	st := state.GetState(ctx)
	userPrompt := &models.UserPrompt{Text: prompt}
	var messages []models.Message
	if ctxId != "" {
		var err error
		if messages, err = st.CtxStore.Load(ctxId); err != nil {
			st.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to get context for id %s: %v", ctxId, err), printer.Reset)
		}
	}
	newCtxId := time.Now().Format(time.RFC3339Nano)

	events := make(chan models.Event, 1)
	go func() {
		defer close(events)

		recorder := &contextRecorder{store: st.CtxStore}
		if len(messages) > st.Settings.MaxMessagesBeforeUserPrompt {
			summary, err := summarizeMessagesWithPrompt(ctx, model, messages,
				consts.SummarizerBeforeLastUserPrompt,
				"Summarizing messages before last user prompt to reduce context length...",
				"Finished summarizing messages.",
				events)
			if err != nil {
				st.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to summarize messages: %v", err), printer.Reset)
				// Only take the last MaxMessagesBeforeUserPrompt messages as context.
				// The recorder and model mutate their own slices, so clone them separately.
				trimmed := messages[len(messages)-st.Settings.MaxMessagesBeforeUserPrompt:]
				recorder.messages = slices.Clone(trimmed)
				messages = slices.Clone(trimmed)
			} else {
				recorder.messages = []models.Message{summary}
				// Keep the last assistant message after the summary so a pending question isn't lost.
				messages = []models.Message{summary, messages[len(messages)-1]}
			}
		} else if ctxId != "" {
			recorder.messages = []models.Message{&models.ContextId{Id: ctxId}}
		}

		// Add current user prompt.
		recorder.messages = append(recorder.messages, userPrompt)
		messages = append(messages, userPrompt)

		innerEvents := util.FeedChan(generateStream(ctx, model, tools, instructions, messages), events)
		events <- &models.Spin{}
		util.StreamActions(innerEvents, recorder.record)
		if err := recorder.saveToStore(newCtxId); err != nil {
			log.Errorf("failed to save context for id %s: %v", newCtxId, err)
		}
		log.Infof("saved context for id %s", newCtxId)
	}()

	return newCtxId, events
}

type compactRequestKey struct{}

type compactRequest struct {
	mu           sync.Mutex
	requested    bool
	instructions string
}

// RequestCompact schedules conversation compaction for the active GenerateStream
// loop bound to ctx. It returns false if ctx is not associated with an active loop.
func RequestCompact(ctx context.Context, instructions string) bool {
	req, ok := ctx.Value(compactRequestKey{}).(*compactRequest)
	if !ok || req == nil {
		return false
	}
	req.mu.Lock()
	defer req.mu.Unlock()
	req.requested = true
	if trimmed := strings.TrimSpace(instructions); trimmed != "" {
		if req.instructions != "" {
			req.instructions += "\n"
		}
		req.instructions += trimmed
	}
	return true
}

func (r *compactRequest) take() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.requested {
		return false, ""
	}
	instructions := r.instructions
	r.requested = false
	r.instructions = ""
	return true, instructions
}

func generateStream(ctx context.Context,
	model llm.Model,
	tools []*tool.Tool,
	instructions []string,
	messages []models.Message,
) <-chan models.Event {
	st := state.GetState(ctx)
	compactReq := &compactRequest{}
	ctx = context.WithValue(ctx, compactRequestKey{}, compactReq)

	events := make(chan models.Event, 1)
	go func() {
		defer close(events)
		for {
			if ctx.Err() != nil {
				return
			}

			var ok bool
			if messages, ok = maybeCompactMessages(ctx, st, model, compactReq, messages, events); !ok {
				return
			}

			curEvents := util.FeedChan(model.GenerateStream(ctx, instructions, tools, messages), events)
			extractor := &toolCallExtractor{toolCalls: &models.ToolCalls{}}
			output := &outputAccumulator{}
			util.StreamActions(curEvents, extractor.extract, output.collect)

			output.finalize()
			messages = append(messages, output.messages...)

			extractor.finalize()
			if len(extractor.toolCalls.Items) == 0 {
				return
			}

			for _, toolCall := range extractor.toolCalls.Items {
				if ctx.Err() != nil {
					return
				}
				toolCall.Result = executeToolCall(ctx, tools, toolCall)
			}

			events <- extractor.toolCalls
			messages = append(messages, extractor.toolCalls)
		}
	}()

	return events
}

// maybeCompactMessages compacts or summarizes messages when requested by the
// compact tool or when the message count exceeds MaxMessagesAfterUserPrompt.
// It returns ok=false only if ctx was cancelled.
func maybeCompactMessages(
	ctx context.Context,
	st *state.State,
	model llm.Model,
	compactReq *compactRequest,
	messages []models.Message,
	events chan<- models.Event,
) ([]models.Message, bool) {
	var (
		next   []models.Message
		err    error
		action string
	)
	if requested, compactInstructions := compactReq.take(); requested {
		action = "compact"
		next, err = compactMessages(ctx, model, messages, compactInstructions, events)
	} else if len(messages) > st.Settings.MaxMessagesAfterUserPrompt {
		action = "summarize"
		next, err = summarizeMessagesAfterUserPrompt(ctx, model, messages, events)
	} else {
		return messages, true
	}

	if err != nil {
		if ctx.Err() != nil {
			return messages, false
		}
		st.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to %s messages: %v", action, err), printer.Reset)
		return messages, true
	}

	events <- &models.Compacted{Messages: slices.Clone(next)}
	events <- &models.Spin{}
	return next, true
}

func executeToolCall(ctx context.Context, tools []*tool.Tool, tc *models.ToolCall) string {
	idx := slices.IndexFunc(tools, func(t *tool.Tool) bool { return t.Name == tc.Name })
	if idx < 0 {
		return fmt.Sprintf("tool %s not found", tc.Name)
	}

	var (
		output []byte
		err    error
	)
	done := make(chan struct{}, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		output, err = tools[idx].Call(ctx, []byte(tc.Args))
	}()
	select {
	case <-ctx.Done():
		return fmt.Sprintf("failed to call tool %s: tool call cancelled: %v", tc.Name, ctx.Err())
	case <-done:
		if err != nil {
			return fmt.Sprintf("failed to call tool %s: %v", tc.Name, err)
		}
		return string(output)
	}
}

type toolCallExtractor struct {
	currentCallId   string
	currentToolName string
	currentArgs     strings.Builder

	toolCalls *models.ToolCalls
}

func (t *toolCallExtractor) extract(event models.Event) {
	switch e := event.(type) {
	case *models.OutputFnCall:
		t.finalize()
		t.currentCallId = e.CallId
		t.currentToolName = e.Name
	case *models.OutputFnArgs:
		t.currentArgs.WriteString(e.Args)
	default:
		t.finalize()
	}
}

func (t *toolCallExtractor) finalize() {
	if t.currentCallId != "" && t.currentToolName != "" {
		args := t.currentArgs.String()
		if args == "" {
			// Some models (e.g. GLM) emit an empty arguments string for
			// tools that take no parameters, instead of "{}" like others do.
			// Default to an empty JSON object so downstream JSON decoding
			// doesn't fail and the call isn't silently dropped.
			args = "{}"
		}
		t.toolCalls.Items = append(t.toolCalls.Items, &models.ToolCall{
			CallId: t.currentCallId,
			Name:   t.currentToolName,
			Args:   args,
		})
	}
	t.currentCallId = ""
	t.currentToolName = ""
	t.currentArgs.Reset()
}

// contextRecorder records the messages of a conversation so that they can be
// used as context in later turns.
type contextRecorder struct {
	outputAccumulator
	store *store.CtxStore
}

func (c *contextRecorder) record(event models.Event) {
	if e, ok := event.(*models.Compacted); ok {
		// Discard any output accumulated from summarization before replacing messages.
		c.outputAccumulator = outputAccumulator{messages: slices.Clone(e.Messages)}
		return
	}
	c.collect(event)
	switch e := event.(type) {
	case *models.OutputErr, *models.ToolCalls:
		c.messages = append(c.messages, e.(models.Message))
	}
}

func (c *contextRecorder) saveToStore(ctxId string) error {
	c.finalize()
	return c.store.Save(ctxId, c.messages)
}

type outputAccumulator struct {
	reasoning strings.Builder
	text      strings.Builder
	messages  []models.Message
}

func (o *outputAccumulator) collect(event models.Event) {
	switch e := event.(type) {
	case *models.OutputReasoning:
		if o.text.Len() > 0 {
			o.finalize()
		}
		o.reasoning.WriteString(e.Reasoning)
	case *models.OutputText:
		if o.reasoning.Len() > 0 {
			o.finalize()
		}
		o.text.WriteString(e.Text)
	default:
		o.finalize()
	}
}

func (o *outputAccumulator) finalize() {
	if o.text.Len() > 0 {
		o.messages = append(o.messages, &models.AgentResponse{Text: o.text.String()})
		o.text.Reset()
	}
	if o.reasoning.Len() > 0 {
		o.messages = append(o.messages, &models.AgentReasoning{Reasoning: o.reasoning.String()})
		o.reasoning.Reset()
	}
}

func summarizeMessagesAfterUserPrompt(ctx context.Context, model llm.Model, allMessages []models.Message, listener chan<- models.Event) ([]models.Message, error) {
	idx := lastUserPromptIndex(allMessages)
	if idx == -1 {
		return nil, fmt.Errorf("no user prompt found in messages")
	}
	summary, err := summarizeMessagesWithPrompt(ctx, model, allMessages,
		consts.SummarizerAfterLastUserPrompt,
		"Summarizing messages after last user prompt to reduce context length...",
		"Finished summarizing messages.",
		listener)
	if err != nil {
		return nil, err
	}
	// Build a fresh slice rather than appending onto a subslice of allMessages.
	return append(slices.Clone(allMessages[:idx+1]), summary), nil
}

func compactMessages(ctx context.Context, model llm.Model, allMessages []models.Message, instructions string, listener chan<- models.Event) ([]models.Message, error) {
	prompt := consts.SummarizerCompact
	if instructions != "" {
		prompt += "\n\n# CALLER COMPACTION INSTRUCTIONS\nThe agent requested special focus for this compaction (follow these unless they conflict with accuracy rules or would drop critical constraints/resumption state):\n" + instructions
	}
	summary, err := summarizeMessagesWithPrompt(ctx, model, allMessages,
		prompt,
		"Compacting conversation messages...",
		"Finished compacting messages.",
		listener)
	if err != nil {
		return nil, err
	}
	// Preserve the latest user prompt (if present) followed by the compacted summary.
	if idx := lastUserPromptIndex(allMessages); idx != -1 {
		return []models.Message{allMessages[idx], summary}, nil
	}
	return []models.Message{summary}, nil
}

func lastUserPromptIndex(messages []models.Message) int {
	for i, msg := range slices.Backward(messages) {
		if _, ok := msg.(*models.UserPrompt); ok {
			return i
		}
	}
	return -1
}

func summarizeMessagesWithPrompt(
	ctx context.Context,
	model llm.Model,
	messages []models.Message,
	summaryPrompt, startLog, endLog string,
	listener chan<- models.Event,
) (*models.Summary, error) {
	listener <- &models.Log{Text: startLog}
	listener <- &models.Spin{}
	defer func() {
		listener <- &models.Log{Text: endLog}
	}()

	// Copy messages before appending the summarization prompt to avoid mutating the caller's backing array.
	request := append(slices.Clone(messages), &models.UserPrompt{Text: summaryPrompt})
	var sb strings.Builder
	for e := range model.GenerateStream(ctx, nil, nil, request) {
		switch ev := e.(type) {
		case *models.OutputText:
			listener <- ev
			sb.WriteString(ev.Text)
		case *models.TokenUsage, *models.OutputReasoning:
			listener <- ev
		case *models.OutputErr:
			return nil, fmt.Errorf("error from summarization: %s", ev.Err)
		}
	}
	return &models.Summary{Text: sb.String()}, nil
}
