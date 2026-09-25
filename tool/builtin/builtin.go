package builtin

import (
	"github.com/speed2exe/ag/tool"
)

func New() map[string]*tool.Tool {
	tools := []*tool.Tool{
		Bash(),
		Agent(),
		Lua(),
	}
	toolByName := make(map[string]*tool.Tool, len(tools))
	for _, t := range tools {
		toolByName[t.Name] = t
	}
	return toolByName
}
