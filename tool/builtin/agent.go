package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/state"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/a2a"
	"github.com/speed2exe/ag/tool/skill"
	"github.com/speed2exe/ag/util"
)

func Agent() *tool.Tool {
	call := func(ctx context.Context, input *models.AgentInput) (*models.AgentOutput, error) {
		st := state.GetState(ctx)
		st.Printer.Indent()
		defer st.Printer.UnIndent()

		hasA2aTool := false
		subTools := make(map[string]*tool.Tool, len(input.ToolNames))
		for _, name := range input.ToolNames {
			subTool, ok := st.ToolsByName[name]
			if !ok {
				return nil, fmt.Errorf("tool %s not found", name)
			}
			subTools[name] = subTool
			if strings.HasPrefix(name, a2a.ToolPrefix) {
				hasA2aTool = true
			}
		}

		subSkills := &skill.Skills{
			SkillByFilePath:        make(map[string]*skill.Skill, len(input.SkillPaths)),
			PreloadSkillByFilePath: st.Skills.PreloadSkillByFilePath,
		}
		for _, path := range input.SkillPaths {
			s, ok := st.Skills.SkillByFilePath[path]
			if !ok {
				return nil, fmt.Errorf("skill %s not found", path)
			}
			subSkills.SkillByFilePath[path] = s
		}

		// Attach a scoped copy of State so the subagent cannot mutate the parent's tools/skills.
		subState := *st
		subState.ToolsByName = subTools
		subState.Skills = subSkills
		subCtx := state.WithState(ctx, &subState)

		var instructions []string
		instructions = append(instructions, skill.BuildPreloadSkillInstructions(subSkills.PreloadSkillByFilePath)...)
		if s := skill.BuildSkillInstructions(subSkills.SkillByFilePath); s != "" {
			instructions = append(instructions, s)
		}
		if hasA2aTool {
			if s := st.A2a.BuildInstructions(); s != "" {
				instructions = append(instructions, s)
			}
		}

		nextCtxId, eventStream := core.GenerateStream(subCtx, st.Llm, util.ToolSortedListFromMap(subTools), instructions, "", input.Prompts)
		eventPrinter := llm.NewEventPrinter(st.Printer)
		lastResponse := &lastResponseText{}
		lastOutputErr := &llm.LastOutputError{}
		util.StreamActions(eventStream,
			eventPrinter.Print,
			lastResponse.collectLastText,
			lastOutputErr.Set,
		)
		eventPrinter.FinishCurrentOutput()
		st.Printer.Print(printer.ColorPurple, fmt.Sprintf("--context-id %s\n", nextCtxId), printer.Reset)

		if lastOutputErr.Err != nil {
			return &models.AgentOutput{
				Error: new(fmt.Sprintf("error generating response: %v", lastOutputErr.Err)),
			}, nil
		}

		return &models.AgentOutput{
			LastResponseText:           lastResponse.text(),
			TotalInputTokens:           eventPrinter.TotalInputTokens,
			TotalInputTokensCached:     eventPrinter.TotalInputTokensCached,
			TotalOutputTokens:          eventPrinter.TotalOutputTokens,
			TotalOutputTokensReasoning: eventPrinter.TotalOutputTokensReasoning,
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.AgentInput, models.AgentOutput]{
		Name:        consts.AgentToolName,
		Description: new("Delegate task to an subagent that DOES NOT have context of the parent agent, and can only use the tools and skills provided."),
		Call:        call,
	}).MustCompile()
}

type lastResponseText struct {
	acc  strings.Builder
	last string
}

func (l *lastResponseText) collectLastText(event models.Event) {
	if e, ok := event.(*models.OutputText); ok {
		l.acc.WriteString(e.Text)
	} else if l.acc.Len() > 0 {
		l.last = l.acc.String()
		l.acc.Reset()
	}
}

func (l *lastResponseText) text() string {
	if l.acc.Len() > 0 {
		return l.acc.String()
	}
	return l.last
}
