//go:build !cgo

package builtin

import (
	"bytes"
	"context"
	"fmt"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"

	"github.com/mmcdole/lunar"
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
		if _, err := s.DoString(input.Name, input.Script); err != nil {
			return nil, fmt.Errorf("failed to execute Lua script: %w", err)
		}
		return &models.LuaOutput{
			Stdout: stdoutBuf.String(),
			Stderr: stderrBuf.String(),
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.LuaInput, models.LuaOutput]{
		Name:        consts.LuaToolName,
		Description: new(`Executes a Lua script. Name: name of the script. Script: Lua code to execute. Returns stdout and stderr.`),
		Call:        call,
	}).MustCompile()
}
