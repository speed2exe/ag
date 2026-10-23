package core

import (
	"context"
	"sync"
	"testing"

	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
)

// fakeModel records the exact slice it was asked to summarize (no copy, so
// shared backing arrays stay observable) and answers with a fixed summary.
type fakeModel struct {
	request     []models.Message
	requestLen  int
	requestSeen bool
	reply       string
}

func (f *fakeModel) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	f.request = messages
	f.requestLen = len(messages)
	f.requestSeen = true
	events := make(chan models.Event, 1)
	events <- &models.OutputText{Text: f.reply}
	close(events)
	return events
}

func newListener() chan models.Event {
	return make(chan models.Event, 64)
}

// The messages returned by the summarizer used to share their backing array
// with the input messages, which the context recorder keeps appending to from
// another goroutine. That duplicated recorded messages, and the duplicated tool
// calls made the summarizer send the same call_id twice, which the Responses API
// rejects with `400 Bad Request {"message":"Duplicate 'call_id': ..."}`.
func TestSummarizeMessagesAfterUserPromptDoesNotAliasInput(t *testing.T) {
	messages := make([]models.Message, 0, 16)
	messages = append(messages,
		&models.UserPrompt{Text: "run it"},
		&models.AgentReasoning{Reasoning: "thinking"},
		&models.ToolCalls{Items: []*models.ToolCall{{CallId: "call_1", Name: "bash", Args: "{}", Result: "one"}}},
	)

	got, err := summarizeMessagesAfterUserPrompt(context.Background(), &fakeModel{reply: "summary"}, messages, newListener())
	if err != nil {
		t.Fatalf("summarizeMessagesAfterUserPrompt() error = %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("summarized messages len = %d, want 2", len(got))
	}
	if _, ok := got[1].(*models.Summary); !ok {
		t.Fatalf("summarized messages[1] = %T, want *models.Summary", got[1])
	}

	// growing the result must not write into the input messages
	got = append(got, &models.AgentResponse{Text: "extra"})
	if _, ok := messages[len(messages)-1].(*models.ToolCalls); !ok {
		t.Fatalf("appending to the summarized messages overwrote the input messages: got %T", messages[len(messages)-1])
	}
}

// The summarization prompt must not be written into the caller's backing array:
// the caller appends to it right after, which dropped the prompt from the
// request and put a tool call there instead.
func TestSummarizeMessagesWithPromptDoesNotMutateInput(t *testing.T) {
	messages := make([]models.Message, 0, 16)
	messages = append(messages,
		&models.UserPrompt{Text: "run it"},
		&models.ToolCalls{Items: []*models.ToolCall{{CallId: "call_1", Name: "bash", Args: "{}", Result: "one"}}},
	)

	model := &fakeModel{reply: "summary"}
	if _, err := summarizeMessagesWithPrompt(context.Background(), model, messages, "summarize", newListener()); err != nil {
		t.Fatalf("summarizeMessagesWithPrompt() error = %v", err)
	}
	if !model.requestSeen {
		t.Fatal("model was not called")
	}
	if want := len(messages) + 1; model.requestLen != want {
		t.Fatalf("summarization request has %d messages, want %d", model.requestLen, want)
	}
	if prompt, ok := model.request[model.requestLen-1].(*models.UserPrompt); !ok || prompt.Text != "summarize" {
		t.Fatalf("summarization request does not end with the summarization prompt: %T", model.request[model.requestLen-1])
	}

	// growing the input messages must not clobber the summarization request
	messages = append(messages, &models.AgentResponse{Text: "clobber"})
	if prompt, ok := model.request[model.requestLen-1].(*models.UserPrompt); !ok || prompt.Text != "summarize" {
		t.Fatalf("summarization prompt was overwritten: %T", model.request[model.requestLen-1])
	}
}

// The recorder is written to from two goroutines: the one consuming the event
// stream (record) and the one driving the model (setMessages, when the messages
// are summarized and compacted). Guarding messages with a mutex keeps both the
// race detector and the saved context consistent.
func TestContextRecorderConcurrentSetAndRecord(t *testing.T) {
	recorder := &ContextRecorder{}
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iterations {
			recorder.setMessages([]models.Message{&models.Summary{Text: "summary"}})
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			recorder.record(&models.OutputErr{Err: "boom"})
		}
	}()
	wg.Wait()

	recorder.record(&models.OutputErr{Err: "last"})
	messages := recorder.messages
	if len(messages) == 0 {
		t.Fatal("recorded messages are empty")
	}
	last, ok := messages[len(messages)-1].(*models.OutputErr)
	if !ok || last.Err != "last" {
		t.Fatalf("last recorded message = %T, want the event recorded after the goroutines finished", messages[len(messages)-1])
	}
}
