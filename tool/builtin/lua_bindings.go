package builtin

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func Lua() *tool.Tool {
	return util.NewJSONTool(
		consts.LuaToolName,
		`Executes Lua scripts and returns stdout, stderr, and the script's return value; multiple returned values are returned as an array.`,
		func(_ context.Context, input *models.LuaInput) (*models.LuaOutput, error) {
			if strings.IndexByte(input.Name, 0) >= 0 {
				return nil, fmt.Errorf("Lua script name contains a NUL byte")
			}
			return executeLua(input)
		},
	)
}

func unwrapLuaResults(values []any) any {
	switch len(values) {
	case 0:
		return nil
	case 1:
		return values[0]
	default:
		return values
	}
}

func luaTableIndex(number float64) (int, bool) {
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number ||
		number < 1 || number >= float64(int(^uint(0)>>1)) {
		return 0, false
	}
	return int(number), true
}

func finalizeLuaTable(object map[string]any, array map[int]any) (any, error) {
	if len(object) > 0 && len(array) > 0 {
		return nil, fmt.Errorf("Lua tool argument tables cannot mix object and array keys")
	}
	if len(array) == 0 {
		return object, nil
	}
	values := make([]any, len(array))
	for i := range values {
		value, ok := array[i+1]
		if !ok {
			return nil, fmt.Errorf("Lua tool argument arrays must use contiguous integer keys starting at 1")
		}
		values[i] = value
	}
	return values, nil
}
