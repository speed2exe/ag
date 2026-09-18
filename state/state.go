package state

import (
	"context"

	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/store"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/skill"
)

type State struct {
	Printer     *printer.Printer
	Settings    *setting.Settings
	Llm         llm.Model
	ToolsByName map[string]*tool.Tool
	Skills      *skill.Skills
	CtxStore    *store.CtxStore
}

func WithState(ctx context.Context, state *State) context.Context {
	return context.WithValue(ctx, "state", state)
}

func GetState(ctx context.Context) *State {
	if state := ctx.Value("state"); state != nil {
		if s, ok := state.(*State); ok {
			return s
		}
	}
	return nil
}
