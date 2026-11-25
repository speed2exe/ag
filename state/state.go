package state

import (
	"context"

	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/store"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/a2a"
	"github.com/speed2exe/ag/tool/skill"
)

type stateKey struct{}

type State struct {
	Printer     *printer.Printer
	Settings    *setting.Settings
	Llm         llm.Model
	ToolsByName map[string]*tool.Tool
	Skills      *skill.Skills
	A2a         *a2a.Clients
	CtxStore    *store.CtxStore
}

func WithState(ctx context.Context, state *State) context.Context {
	return context.WithValue(ctx, stateKey{}, state)
}

func GetState(ctx context.Context) *State {
	s, _ := ctx.Value(stateKey{}).(*State)
	return s
}
