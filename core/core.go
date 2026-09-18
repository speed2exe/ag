package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
	state := state.GetState(ctx)
	userPrompt := &models.UserPrompt{Text: prompt}
	messages, err := getMessagesFromContextId(state.CtxStore, ctxId)
	if err != nil {
		state.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to get context for id %s: %v", ctxId, err), printer.Reset)
	}
	newCtxId := time.Now().Format(time.RFC3339Nano)

	events := make(chan models.Event, 1)
	go func() {
		defer close(events)

		contextRecorder := &ContextRecorder{store: state.CtxStore}
		if len(messages) > state.Settings.MaxMessagesBeforeUserPrompt {
			summary, err := summarizeMessagesBeforeUserPrompt(ctx, model, messages, events)
			if err != nil {
				state.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to summarize messages: %v", err), printer.Reset)
				// just take the last 16 messages
				contextRecorder.messages = messages[len(messages)-16:]
				messages = messages[len(messages)-16:]
			} else {
				contextRecorder.messages = []models.Message{summary}
				// replace messages with summary
				messages = append(messages[:0], summary)
			}
		} else if ctxId != "" {
			contextRecorder.messages = []models.Message{&models.ContextId{Id: ctxId}}
		}

		// lastly, add in current user prompt
		contextRecorder.messages = append(contextRecorder.messages, userPrompt)
		messages = append(messages, userPrompt)

		innerEvents := util.FeedChan(generateStream(ctx, model, tools, instructions, messages, contextRecorder), events)
		events <- &models.Spin{}
		util.StreamActions(innerEvents, contextRecorder.record)
		if err := contextRecorder.saveToStore(newCtxId); err != nil {
			log.Errorf("failed to save context for id %s: %v", newCtxId, err)
		}
		log.Infof("saved context for id %s", newCtxId)
	}()

	return newCtxId, events
}

func generateStream(ctx context.Context,
	model llm.Model,
	tools []*tool.Tool,
	instructions []string,
	messages []models.Message,
	contextRecorder *ContextRecorder,
) <-chan models.Event {
	state := state.GetState(ctx)

	events := make(chan models.Event, 1)
	go func() {
		defer close(events)
		for {
			// check context cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			if len(messages) > state.Settings.MaxMessagesAfterUserPrompt {
				summary, err := summarizeMessagesAfterUserPrompt(ctx, model, messages, events)
				if err != nil {
					state.Printer.Print(printer.ColorRed, fmt.Sprintf("failed to summarize messages: %v", err), printer.Reset)
				} else {
					messages = summary
					contextRecorder.messages = summary
				}
			}

			curEvents := util.FeedChan(model.GenerateStream(ctx, instructions, tools, messages), events)
			toolCallExtractor := &ToolCallExtractor{}
			lastOutputErr := &llm.LastOutputError{}
			util.StreamActions(curEvents, toolCallExtractor.extract, lastOutputErr.Set)

			toolCallExtractor.finalize()
			toolCalls := toolCallExtractor.acc
			if len(toolCalls) == 0 {
				return
			}

			for _, toolCall := range toolCalls {
				// check context cancellation
				select {
				case <-ctx.Done():
					return
				default:
				}

				tool, ok := lookupToolByName(tools, toolCall.Name)
				if !ok {
					if toolCaller, ok := model.(llm.ToolCaller); ok {
						result, found, err := execLlmToolCall(ctx, toolCaller, toolCall.Name, toolCall.Args)
						if !found {
							toolCall.Result = fmt.Sprintf("tool %s not found", toolCall.Name)
						} else if err != nil {
							toolCall.Result = fmt.Sprintf("failed to call tool %s: %v", toolCall.Name, err)
						} else {
							toolCall.Result = result
						}
					} else {
						toolCall.Result = fmt.Sprintf("tool %s not found", toolCall.Name)
					}
				} else {
					output, err := execToolCall(ctx, tool, []byte(toolCall.Args))
					if err != nil {
						toolCall.Result = fmt.Sprintf("failed to call tool %s: %v", toolCall.Name, err)
					} else {
						toolCall.Result = string(output)
					}
				}
				messages = append(messages, toolCall)
				events <- toolCall
			}
		}
	}()

	return events
}

func execLlmToolCall(ctx context.Context, llmToolCaller llm.ToolCaller, toolName string, toolArgs string) (string, bool, error) {
	var (
		result string
		found  bool
		err    error
	)
	done := make(chan struct{}, 1)
	go func() {
		result, found, err = llmToolCaller.CallTool(ctx, toolName, toolArgs)
		done <- struct{}{}
	}()
	select {
	case <-ctx.Done():
		return "", false, fmt.Errorf("llm tool call cancelled: %w", ctx.Err())
	case <-done:
		return result, found, err
	}
}

func execToolCall(ctx context.Context, tool *tool.Tool, args []byte) ([]byte, error) {
	var (
		result []byte
		err    error
	)
	done := make(chan struct{}, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		result, err = tool.Call(ctx, args)
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("tool call cancelled: %w", ctx.Err())
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

	acc []*models.ToolCallResult
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
		t.acc = append(t.acc, &models.ToolCallResult{
			CallId: t.currentCallId,
			Name:   t.currentToolName,
			Args:   args,
		})
	}
	t.currentCallId = ""
	t.currentToolName = ""
	t.currentArgs.Reset()
}

// TODO: serialize and deserialize
type ContextRecorder struct {
	store    *store.CtxStore
	messages []models.Message

	outputTextAcc strings.Builder
}

func (c *ContextRecorder) record(event models.Event) {
	switch e := event.(type) {
	case *models.OutputText:
		c.outputTextAcc.WriteString(e.Text)
	default:
		c.finalizeText()
		switch e := event.(type) {
		case *models.OutputErr:
			c.messages = append(c.messages, e)
		case *models.ToolCallResult:
			c.messages = append(c.messages, e)
		}
	}
}

func (c *ContextRecorder) finalizeText() {
	if c.outputTextAcc.Len() > 0 {
		c.messages = append(c.messages, &models.AgentResponse{Text: c.outputTextAcc.String()})
		c.outputTextAcc.Reset()
	}
}

func (c *ContextRecorder) saveToStore(ctxId string) error {
	c.finalizeText()
	return c.store.Save(ctxId, c.messages)
}

func summarizeMessagesBeforeUserPrompt(ctx context.Context, model llm.Model, messagesBeforeUserPrompt []models.Message, listener chan<- models.Event) (*models.Summary, error) {
	listener <- &models.Log{Text: "Summarizing messages before last user prompt to reduce context length..."}
	listener <- &models.Spin{}
	defer func() {
		listener <- &models.Log{Text: "Finished summarizing messages."}
	}()

	summary, err := summarizeMessagesWithPrompt(ctx, model, messagesBeforeUserPrompt, consts.SummarizerBeforeLastUserPrompt, listener)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize messages: %v", err)
	}
	return summary, nil
}

func summarizeMessagesAfterUserPrompt(ctx context.Context, model llm.Model, allMessages []models.Message, listener chan<- models.Event) ([]models.Message, error) {
	listener <- &models.Log{Text: "Summarizing messages after last user prompt to reduce context length..."}
	listener <- &models.Spin{}
	defer func() {
		listener <- &models.Log{Text: "Finished summarizing messages."}
	}()

	// get index of last user prompt
	lastUserPromptIndex := -1
	for i, allMessage := range slices.Backward(allMessages) {
		if _, ok := allMessage.(*models.UserPrompt); ok {
			lastUserPromptIndex = i
			break
		}
	}
	if lastUserPromptIndex == -1 {
		return nil, fmt.Errorf("no user prompt found in messages")
	}

	summary, err := summarizeMessagesWithPrompt(ctx, model, allMessages, consts.SummarizerAfterLastUserPrompt, listener)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize messages: %v", err)
	}
	return append(allMessages[:lastUserPromptIndex+1], summary), nil
}

func summarizeMessagesWithPrompt(ctx context.Context, model llm.Model, messages []models.Message, summaryPrompt string, listener chan<- models.Event) (*models.Summary, error) {
	summaryEvents := model.GenerateStream(ctx, nil, nil, append(messages, &models.UserPrompt{Text: summaryPrompt}))
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
