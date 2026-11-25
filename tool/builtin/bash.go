package builtin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func Bash() *tool.Tool {
	return util.NewJSONTool(
		consts.BashToolName,
		`Runs a bash command (bash -c <cmd>).`,
		func(ctx context.Context, input *models.BashInput) (*models.BashOutput, error) {
			cmd := exec.CommandContext(ctx, "bash", "-c", input.Command)

			var stdoutBuf, stderrBuf bytes.Buffer
			cmd.Stdout = &stdoutBuf
			cmd.Stderr = &stderrBuf

			exitCode := 0
			if err := cmd.Run(); err != nil {
				if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
					exitCode = exitErr.ExitCode()
				} else {
					return nil, fmt.Errorf("failed to run bash command: %w", err)
				}
			}

			return &models.BashOutput{
				Stdout:   stdoutBuf.String(),
				Stderr:   stderrBuf.String(),
				ExitCode: exitCode,
			}, nil
		},
	)
}
