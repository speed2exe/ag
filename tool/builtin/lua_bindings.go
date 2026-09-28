package builtin

import (
	"fmt"
	"strings"

	"github.com/speed2exe/ag/models"
)

func validateLuaInput(input *models.LuaInput) error {
	if strings.IndexByte(input.Name, 0) >= 0 {
		return fmt.Errorf("Lua script name contains a NUL byte")
	}
	return nil
}
