package builtin

import (
	"bytes"
	"context"
	"fmt"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"

	lua "github.com/mmcdole/lunar"
)

func Lua() *tool.Tool {
	call := func(ctx context.Context, input *models.LuaInput) (*models.LuaOutput, error) {
		var stdoutBuf, stderrBuf bytes.Buffer
		s, err := lua.New(lua.Options{
			Libraries: lua.FullLibraries(),
			Stdout:    &stdoutBuf,
			Stderr:    &stderrBuf,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Lua state: %w", err)
		}
		res, err := s.DoString(input.Name, input.Script)
		if err != nil {
			return nil, fmt.Errorf("failed to execute Lua script: %w", err)
		}

		// Convert the result to a string representation
		resultStrings := make([]string, len(res))
		for i, v := range res {
			resultStrings[i] = v.String()
		}

		return &models.LuaOutput{
			Return: resultStrings,
			Stdout: stdoutBuf.String(),
			Stderr: stderrBuf.String(),
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.LuaInput, models.LuaOutput]{
		Name:        consts.LuaToolName,
		Description: new(`Executes a Lua script. Name: name of the script. Script: Lua code to execute.`),
		Call:        call,
	}).MustCompile()
}
