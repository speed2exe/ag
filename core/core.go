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
	messages, err := getMessagesFromContextId(st.CtxStore, ctxId)
	if err != nil {
		st.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to get context for id %s: %v", ctxId, err), printer.Reset)
	}
	newCtxId := time.Now().Format(time.RFC3339Nano)

	events := make(chan models.Event, 1)
	go func() {
		defer close(events)

		contextRecorder := &ContextRecorder{store: st.CtxStore}
		if len(messages) > st.Settings.MaxMessagesBeforeUserPrompt {
			summary, err := summarizeMessagesBeforeUserPrompt(ctx, model, messages, events)
			if err != nil {
				st.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to summarize messages: %v", err), printer.Reset)
				// Only take the last MaxMessagesBeforeUserPrompt messages as context.
				// The recorder and model mutate their own slices, so clone them separately.
				trimmed := messages[len(messages)-st.Settings.MaxMessagesBeforeUserPrompt:]
				contextRecorder.messages = slices.Clone(trimmed)
				messages = slices.Clone(trimmed)
			} else {
				contextRecorder.messages = []models.Message{summary}
				// Keep the last assistant message after the summary so a pending question isn't lost.
				messages = append(make([]models.Message, 0, 2), summary, messages[len(messages)-1])
			}
		} else if ctxId != "" {
			contextRecorder.messages = []models.Message{&models.ContextId{Id: ctxId}}
		}

		// Add current user prompt.
		contextRecorder.messages = append(contextRecorder.messages, userPrompt)
		messages = append(messages, userPrompt)

		innerEvents := util.FeedChan(generateStream(ctx, model, tools, instructions, messages), events)
		events <- &models.Spin{}
		util.StreamActions(innerEvents, contextRecorder.record)
		if err := contextRecorder.saveToStore(newCtxId); err != nil {
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
			toolCallExtractor := &ToolCallExtractor{toolCalls: &models.ToolCalls{}}
			outputCollector := &OutputCollector{}
			util.StreamActions(curEvents, toolCallExtractor.extract, outputCollector.collect)

			outputCollector.finalize()
			messages = append(messages, outputCollector.messages...)

			toolCallExtractor.finalize()
			toolCalls := toolCallExtractor.toolCalls
			if len(toolCalls.Items) == 0 {
				return
			}

			for _, toolCall := range toolCalls.Items {
				if ctx.Err() != nil {
					return
				}
				toolCall.Result = executeToolCall(ctx, model, tools, toolCall)
			}

			events <- toolCalls
			messages = append(messages, toolCalls)
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

func executeToolCall(ctx context.Context, model llm.Model, tools []*tool.Tool, tc *models.ToolCall) string {
	if t, ok := lookupToolByName(tools, tc.Name); ok {
		output, err := runWithContext(ctx, "tool call cancelled", func() ([]byte, error) {
			return t.Call(ctx, []byte(tc.Args))
		})
		if err != nil {
			return fmt.Sprintf("failed to call tool %s: %v", tc.Name, err)
		}
		return string(output)
	}

	if toolCaller, ok := model.(llm.ToolCaller); ok {
		type llmResult struct {
			output string
			found  bool
		}
		res, err := runWithContext(ctx, "llm tool call cancelled", func() (llmResult, error) {
			out, found, err := toolCaller.CallTool(ctx, tc.Name, tc.Args)
			return llmResult{output: out, found: found}, err
		})
		if err != nil {
			return fmt.Sprintf("failed to call tool %s: %v", tc.Name, err)
		}
		if res.found {
			return res.output
		}
	}

	return fmt.Sprintf("tool %s not found", tc.Name)
}

func runWithContext[T any](ctx context.Context, cancelMsg string, fn func() (T, error)) (T, error) {
	var (
		result T
		err    error
	)
	done := make(chan struct{}, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		result, err = fn()
	}()
	select {
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("%s: %w", cancelMsg, ctx.Err())
	case <-done:
		return result, err
	}
}

func getMessagesFromContextId(ctxStore *store.CtxStore, contextId string) ([]models.Message, error) {
	if contextId == "" {
		return nil, nil
	}
	msgs, err := ctxStore.Load(contextId)
	if err != nil {
		return nil, fmt.Errorf("failed to load context for id %s: %w", contextId, err)
	}
	return msgs, nil
}

type ToolCallExtractor struct {
	currentCallId   string
	currentToolName string
	currentArgs     strings.Builder

	toolCalls *models.ToolCalls
}

func (t *ToolCallExtractor) extract(event models.Event) {
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

func (t *ToolCallExtractor) finalize() {
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

// ContextRecorder records the messages of a conversation so that they can be
// used as context in later turns.
type ContextRecorder struct {
	outputAccumulator outputAccumulator
	store             *store.CtxStore
	messages          []models.Message
}

func (c *ContextRecorder) record(event models.Event) {
	if e, ok := event.(*models.Compacted); ok {
		// Discard any output accumulated from summarization before replacing messages.
		c.outputAccumulator = outputAccumulator{}
		c.messages = slices.Clone(e.Messages)
		return
	}
	c.messages = append(c.messages, c.outputAccumulator.collect(event)...)
	switch e := event.(type) {
	case *models.OutputErr, *models.ToolCalls:
		c.messages = append(c.messages, e.(models.Message))
	}
}

func (c *ContextRecorder) saveToStore(ctxId string) error {
	c.messages = append(c.messages, c.outputAccumulator.finalize()...)
	return c.store.Save(ctxId, c.messages)
}

type outputAccumulator struct {
	outputReasoningAcc strings.Builder
	outputTextAcc      strings.Builder
}

func (o *outputAccumulator) collect(event models.Event) []models.Message {
	switch e := event.(type) {
	case *models.OutputReasoning:
		var messages []models.Message
		if o.outputTextAcc.Len() > 0 {
			messages = o.finalize()
		}
		o.outputReasoningAcc.WriteString(e.Reasoning)
		return messages
	case *models.OutputText:
		var messages []models.Message
		if o.outputReasoningAcc.Len() > 0 {
			messages = o.finalize()
		}
		o.outputTextAcc.WriteString(e.Text)
		return messages
	default:
		return o.finalize()
	}
}

func (o *outputAccumulator) finalize() []models.Message {
	var messages []models.Message
	if o.outputTextAcc.Len() > 0 {
		messages = append(messages, &models.AgentResponse{Text: o.outputTextAcc.String()})
		o.outputTextAcc.Reset()
	}
	if o.outputReasoningAcc.Len() > 0 {
		messages = append(messages, &models.AgentReasoning{Reasoning: o.outputReasoningAcc.String()})
		o.outputReasoningAcc.Reset()
	}
	return messages
}

// extracts reasoning and output text
type OutputCollector struct {
	outputAccumulator outputAccumulator
	messages          []models.Message
}

func (o *OutputCollector) collect(event models.Event) {
	o.messages = append(o.messages, o.outputAccumulator.collect(event)...)
}

func (o *OutputCollector) finalize() {
	o.messages = append(o.messages, o.outputAccumulator.finalize()...)
}

func summarizeMessagesBeforeUserPrompt(ctx context.Context, model llm.Model, messagesBeforeUserPrompt []models.Message, listener chan<- models.Event) (*models.Summary, error) {
	listener <- &models.Log{Text: "Summarizing messages before last user prompt to reduce context length..."}
	listener <- &models.Spin{}
	defer func() {
		listener <- &models.Log{Text: "Finished summarizing messages."}
	}()

	return summarizeMessagesWithPrompt(ctx, model, messagesBeforeUserPrompt, consts.SummarizerBeforeLastUserPrompt, listener)
}

func summarizeMessagesAfterUserPrompt(ctx context.Context, model llm.Model, allMessages []models.Message, listener chan<- models.Event) ([]models.Message, error) {
	listener <- &models.Log{Text: "Summarizing messages after last user prompt to reduce context length..."}
	listener <- &models.Spin{}
	defer func() {
		listener <- &models.Log{Text: "Finished summarizing messages."}
	}()

	idx := lastUserPromptIndex(allMessages)
	if idx == -1 {
		return nil, fmt.Errorf("no user prompt found in messages")
	}

	summary, err := summarizeMessagesWithPrompt(ctx, model, allMessages, consts.SummarizerAfterLastUserPrompt, listener)
	if err != nil {
		return nil, err
	}
	// Build a fresh slice rather than appending onto a subslice of allMessages.
	messages := make([]models.Message, 0, idx+2)
	messages = append(messages, allMessages[:idx+1]...)
	messages = append(messages, summary)
	return messages, nil
}

func compactMessages(ctx context.Context, model llm.Model, allMessages []models.Message, instructions string, listener chan<- models.Event) ([]models.Message, error) {
	listener <- &models.Log{Text: "Compacting conversation messages..."}
	listener <- &models.Spin{}
	defer func() {
		listener <- &models.Log{Text: "Finished compacting messages."}
	}()

	prompt := consts.SummarizerCompact
	if instructions != "" {
		prompt += "\n\n# CALLER COMPACTION INSTRUCTIONS\nThe agent requested special focus for this compaction (follow these unless they conflict with accuracy rules or would drop critical constraints/resumption state):\n" + instructions
	}

	summary, err := summarizeMessagesWithPrompt(ctx, model, allMessages, prompt, listener)
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

func summarizeMessagesWithPrompt(ctx context.Context, model llm.Model, messages []models.Message, summaryPrompt string, listener chan<- models.Event) (*models.Summary, error) {
	// Copy messages before appending the summarization prompt to avoid mutating the caller's backing array.
	request := make([]models.Message, 0, len(messages)+1)
	request = append(request, messages...)
	request = append(request, &models.UserPrompt{Text: summaryPrompt})
	summaryEvents := model.GenerateStream(ctx, nil, nil, request)
	var sb strings.Builder
	for e := range summaryEvents {
		switch ev := e.(type) {
		case *models.OutputText:
			listener <- ev
			sb.WriteString(ev.Text)
		case *models.TokenUsage, *models.OutputReasoning, *models.OutputUnknown:
			listener <- ev
		case *models.OutputErr:
			return nil, fmt.Errorf("error from summarization: %s", ev.Err)
		}
	}
	return &models.Summary{Text: sb.String()}, nil
}

func lookupToolByName(tools []*tool.Tool, name string) (*tool.Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}
