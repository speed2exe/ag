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

func New(s *setting.Settings) (Model, error) {
	switch s.LlmProvider {
	case setting.LlmProviderAnthropic:
		return anthropic.New(s), nil
	case setting.LlmProviderOpenAI:
		return openai.New(s), nil
	case setting.LlmProviderGemini:
		c, err := gemini.New(s)
		if err != nil {
			return nil, fmt.Errorf("failed to create gemini client: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", s.LlmProvider)
	}
}
