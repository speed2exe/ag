package builtin

import (
	"fmt"
	"strings"

	"github.com/speed2exe/ag/tool"
)

func luaToolDescription(tools []*tool.Tool) *string {
	var description strings.Builder
	description.WriteString("Executes Lua scripts with the provided tools exposed as global Lua functions. Call each function with a Lua table of JSON-compatible arguments; JSON results become Lua values, plain-text MCP results become strings, and tool errors raise Lua errors. Returns stdout, stderr, and the script's return value; multiple returned values are returned as an array.")
	if len(tools) > 0 {
		description.WriteString("\nAvailable functions:")
	}
	for _, currentTool := range tools {
		if currentTool == nil {
			continue
		}
		fmt.Fprintf(&description, "\n- %s", currentTool.Name)
		if currentTool.Description != nil {
			fmt.Fprintf(&description, ": %s", *currentTool.Description)
		}
		if len(currentTool.InputSchema) > 0 {
			fmt.Fprintf(&description, "\n  Input schema: %s", currentTool.InputSchema)
		}
	}
	text := description.String()
	return &text
}
