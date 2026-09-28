package core

import (
	"testing"

	"github.com/speed2exe/ag/models"
)

func TestOutputCollectorGroupsConsecutiveOutputEvents(t *testing.T) {
	collector := &OutputCollector{}
	collector.collect(&models.OutputText{Text: "first "})
	collector.collect(&models.OutputText{Text: "second"})
	collector.collect(&models.OutputReasoning{Reasoning: "thought"})
	collector.collect(&models.OutputUnknown{})
	collector.finalize()

	if len(collector.messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(collector.messages))
	}
	if got := collector.messages[0].(*models.AgentResponse).Text; got != "first second" {
		t.Errorf("response text = %q, want %q", got, "first second")
	}
	if got := collector.messages[1].(*models.AgentReasoning).Reasoning; got != "thought" {
		t.Errorf("reasoning = %q, want %q", got, "thought")
	}
}

func TestContextRecorderFlushesOutputBeforeMessages(t *testing.T) {
	recorder := &ContextRecorder{}
	recorder.record(&models.OutputText{Text: "response"})
	recorder.record(&models.ToolCalls{})
	recorder.finalizeText()

	if len(recorder.messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(recorder.messages))
	}
	if got := recorder.messages[0].(*models.AgentResponse).Text; got != "response" {
		t.Errorf("response text = %q, want %q", got, "response")
	}
	if _, ok := recorder.messages[1].(*models.ToolCalls); !ok {
		t.Errorf("second message has type %T, want *models.ToolCalls", recorder.messages[1])
	}
}
