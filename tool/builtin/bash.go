package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func Bash() *tool.Tool {
	call := func(ctx context.Context, input *models.BashInput) (*models.BashOutput, error) {
		cmd := exec.CommandContext(ctx, "bash", "-c", input.Command)

		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf

		execErr := cmd.Run()
		exitCode := 0
		if execErr != nil {
			if exitErr, ok := execErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				return nil, fmt.Errorf("failed to run bash command: %w", execErr)
			}
		}

		return &models.BashOutput{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.BashInput, models.BashOutput]{
		Name:        consts.BashToolName,
		Description: new(`Runs a bash command (bash -c <cmd>)`),
		Call:        call,
	}).MustCompile()
}
