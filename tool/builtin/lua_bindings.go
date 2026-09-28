package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
)

func validateLuaInput(input *models.LuaInput) error {
	if strings.IndexByte(input.Name, 0) >= 0 {
		return fmt.Errorf("Lua script name contains a NUL byte")
	}
	return nil
}

func validateLuaTools(tools []*tool.Tool) error {
	seen := make(map[string]struct{}, len(tools))
	for index, currentTool := range tools {
		if currentTool == nil || currentTool.Call == nil {
			return fmt.Errorf("Lua tool at index %d is nil or has no call function", index)
		}
		if currentTool.Name == "" || strings.IndexByte(currentTool.Name, 0) >= 0 {
			return fmt.Errorf("Lua tool at index %d has an invalid name", index)
		}
		if _, exists := seen[currentTool.Name]; exists {
			return fmt.Errorf("duplicate Lua tool name %q", currentTool.Name)
		}
		seen[currentTool.Name] = struct{}{}
	}
	return nil
}

func callLuaTool(ctx context.Context, currentTool *tool.Tool, arguments any) (any, error) {
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool arguments: %w", err)
	}
	resultJSON, err := currentTool.Call(ctx, argumentsJSON)
	if err != nil {
		return nil, fmt.Errorf("tool %s failed: %w", currentTool.Name, err)
	}
	result, err := decodeLuaJSON(resultJSON)
	if err == nil {
		return result, nil
	}
	if currentTool.LuaOutputAsText {
		return string(resultJSON), nil
	}
	return nil, fmt.Errorf("tool %s returned invalid JSON: %w", currentTool.Name, err)
}
