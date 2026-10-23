package openai

import (
	"context"
	"slices"
	"testing"

	"github.com/speed2exe/ag/models"
)

func inputCallIds(messages []models.Message) (calls, outputs []string) {
	input := toResponsesInput(context.Background(), messages)
	for _, item := range input {
		if item.OfFunctionCall != nil {
			calls = append(calls, item.OfFunctionCall.CallID)
		}
		if item.OfFunctionCallOutput != nil {
			outputs = append(outputs, item.OfFunctionCallOutput.CallID)
		}
	}
	return calls, outputs
}

func TestToResponsesInputDropsDuplicateCallIds(t *testing.T) {
	duplicated := &models.ToolCalls{Items: []*models.ToolCall{
		{CallId: "call_1", Name: "bash", Args: `{"command":"echo one"}`, Result: "one"},
	}}
	messages := []models.Message{
		&models.UserPrompt{Text: "run it"},
		duplicated,
		// the same tool calls can end up twice in a context, see the shared
		// backing array bug that used to duplicate recorded messages
		duplicated,
		&models.ToolCalls{Items: []*models.ToolCall{
			{CallId: "call_1", Name: "bash", Args: `{"command":"echo one"}`, Result: "one"},
			{CallId: "call_2", Name: "bash", Args: `{"command":"echo two"}`, Result: "two"},
		}},
	}

	calls, outputs := inputCallIds(messages)
	if want := []string{"call_1", "call_2"}; !slices.Equal(calls, want) {
		t.Fatalf("function_call call ids = %v, want %v", calls, want)
	}
	if want := []string{"call_1", "call_2"}; !slices.Equal(outputs, want) {
		t.Fatalf("function_call_output call ids = %v, want %v", outputs, want)
	}
}

func TestToResponsesInputKeepsDistinctCallIds(t *testing.T) {
	messages := []models.Message{
		&models.UserPrompt{Text: "run it"},
		&models.ToolCalls{Items: []*models.ToolCall{
			{CallId: "call_1", Name: "bash", Args: "{}", Result: "one"},
			{CallId: "call_2", Name: "bash", Args: "{}", Result: "two"},
		}},
	}

	calls, outputs := inputCallIds(messages)
	if want := []string{"call_1", "call_2"}; !slices.Equal(calls, want) {
		t.Fatalf("function_call call ids = %v, want %v", calls, want)
	}
	if want := []string{"call_1", "call_2"}; !slices.Equal(outputs, want) {
		t.Fatalf("function_call_output call ids = %v, want %v", outputs, want)
	}
}
