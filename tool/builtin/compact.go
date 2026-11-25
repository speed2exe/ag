package builtin

import (
	"context"
	"fmt"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func Compact() *tool.Tool {
	return util.NewJSONTool(
		consts.CompactToolName,
		`Compact the conversation history into a summary to reduce context size. Use this when the context becomes long or noisy (e.g., after finishing a subtask or accumulating large tool outputs), or when the user asks to compact the conversation.`,
		func(ctx context.Context, input *models.CompactInput) (*models.CompactOutput, error) {
			if !core.RequestCompact(ctx, input.Instructions) {
				return nil, fmt.Errorf("no active conversation to compact")
			}
			return &models.CompactOutput{
				Compacted: true,
				Message:   "Conversation compaction scheduled; history will be compacted before the next turn.",
			}, nil
		},
	)
}
