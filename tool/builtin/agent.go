package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/state"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/skill"
	"github.com/speed2exe/ag/util"
)

func Agent() *tool.Tool {
	call := func(ctx context.Context, input *models.AgentInput) (*models.AgentOutput, error) {
		state := state.GetState(ctx)
		state.Printer.Indent()
		defer state.Printer.UnIndent()

		// set sub tools
		subTaskToolByName := make(map[string]*tool.Tool, len(input.ToolNames))
		for _, subToolName := range input.ToolNames {
			if subTool, ok := state.ToolsByName[subToolName]; ok {
				subTaskToolByName[subToolName] = subTool
			} else {
				return nil, fmt.Errorf("tool %s not found", subToolName)
			}
		}
		orginalTools := state.ToolsByName
		state.ToolsByName = subTaskToolByName
		defer func() {
			state.ToolsByName = orginalTools
		}()

		// set sub skills
		subSkill := &skill.Skills{
			SkillByFilePath:        map[string]*skill.Skill{},
			PreloadSkillByFilePath: state.Skills.PreloadSkillByFilePath,
		}
		for _, subSkillPath := range input.SkillPaths {
			if skill, ok := state.Skills.SkillByFilePath[subSkillPath]; ok {
				subSkill.SkillByFilePath[subSkillPath] = skill
			} else {
				return nil, fmt.Errorf("skill %s not found", subSkillPath)
			}
		}
		orginalSkills := state.Skills
		state.Skills = subSkill
		defer func() {
			state.Skills = orginalSkills
		}()

		var instructions []string
		if ss := skill.BuildPreloadSkillInstructions(subSkill.PreloadSkillByFilePath); len(ss) > 0 {
			instructions = append(instructions, ss...)
		}
		if s := skill.BuildSkillInstructions(subSkill.SkillByFilePath); s != "" {
			instructions = append(instructions, s)
		}
		nextCtxId, eventStream := core.GenerateStream(ctx, state.Llm, util.ToolSortedListFromMap(subTaskToolByName), instructions, "", input.Prompts)
		eventPrinter := llm.NewEventPrinter(state.Printer)
		lastResponseText := &lastResponseText{}

		lastOutputErr := &llm.LastOutputError{}
		util.StreamActions(eventStream,
			eventPrinter.Print,
			lastResponseText.collectLastText,
			lastOutputErr.Set,
		)
		eventPrinter.FinishCurrentOutput()
		state.Printer.Print(printer.ColorPurple, fmt.Sprintf("--context-id %s\n", nextCtxId), printer.Reset)

		if lastOutputErr.Err != nil {
			return &models.AgentOutput{
				Error: new(fmt.Sprintf("error generating response: %v", lastOutputErr.Err)),
			}, nil
		}

		finalAnswer := lastResponseText.last
		return &models.AgentOutput{
			LastResponseText:           finalAnswer,
			TotalInputTokens:           eventPrinter.TotalInputTokens,
			TotalInputTokensCached:     eventPrinter.TotalInputTokensCached,
			TotalOutputTokens:          eventPrinter.TotalOutputTokens,
			TotalOutputTokensReasoning: eventPrinter.TotalOutputTokensReasoning,
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.AgentInput, models.AgentOutput]{
		Name:        "agent",
		Description: new("Delegate task to an subagent that DOES NOT have context of the parent agent, and can only use the tools and skills provided."),
		Call:        call,
	}).MustCompile()
}

type lastResponseText struct {
	acc  strings.Builder
	last string
}

func (l *lastResponseText) collectLastText(event models.Event) {
	switch e := event.(type) {
	case *models.OutputText:
		l.acc.WriteString(e.Text)
	default:
		if l.acc.Len() > 0 {
			l.last = l.acc.String()
			l.acc.Reset()
		}
	}
}
