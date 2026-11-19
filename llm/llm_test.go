package llm

import (
	"testing"

	"github.com/speed2exe/ag/setting"
)

func TestNewGeminiProvider(t *testing.T) {
	m, err := New(nil, &setting.Settings{
		LlmProvider:  setting.LlmProviderGemini,
		GeminiApiKey: "test-key",
		GeminiModel:  "gemini-4-hydrogen",
	})
	if err != nil {
		t.Fatalf("New(gemini) error = %v", err)
	}
	if m == nil {
		t.Fatal("New(gemini) returned nil model")
	}
	if _, ok := m.(ToolCaller); !ok {
		t.Fatalf("New(gemini) returned %T which does not implement ToolCaller", m)
	}
}
