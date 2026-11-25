package llm

import (
	"context"
	"fmt"

	"github.com/speed2exe/ag/llm/anthropic"
	"github.com/speed2exe/ag/llm/gemini"
	"github.com/speed2exe/ag/llm/openai"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
)

type Model interface {
	GenerateStream(
		ctx context.Context,
		instructions []string,
		tools []*tool.Tool,
		messages []models.Message,
	) <-chan models.Event
}

type ToolCaller interface {
	CallTool(ctx context.Context, toolName, args string) (string, bool, error)
}

var (
	_ ToolCaller = (*anthropic.Client)(nil)
	_ ToolCaller = (*openai.Client)(nil)
	_ ToolCaller = (*gemini.Client)(nil)
)

func New(log func(msg string), s *setting.Settings) (Model, error) {
	switch s.LlmProvider {
	case setting.LlmProviderAnthropic:
		c, err := anthropic.New(log, s)
		if err != nil {
			return nil, fmt.Errorf("failed to create anthropic client: %w", err)
		}
		return c, nil
	case setting.LlmProviderOpenAI:
		c, err := openai.New(log, s)
		if err != nil {
			return nil, fmt.Errorf("failed to create openai client: %w", err)
		}
		return c, nil
	case setting.LlmProviderGemini:
		c, err := gemini.New(log, s)
		if err != nil {
			return nil, fmt.Errorf("failed to create gemini client: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", s.LlmProvider)
	}
}
