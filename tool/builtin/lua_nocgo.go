//go:build !cgo

package builtin

import (
	"bytes"
	"context"
	"fmt"
	"math"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"

	"github.com/mmcdole/lunar"
)

func Lua() *tool.Tool {
	call := func(_ context.Context, input *models.LuaInput) (*models.LuaOutput, error) {
		if err := validateLuaInput(input); err != nil {
			return nil, err
		}

		var stdoutBuf, stderrBuf bytes.Buffer
		s, err := lua.New(lua.Options{
			Libraries: lua.FullLibraries(),
			Stdout:    &stdoutBuf,
			Stderr:    &stderrBuf,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Lua state: %w", err)
		}
		defer s.Close()

		results, err := s.DoString(input.Name, input.Script)
		if err != nil {
			return nil, fmt.Errorf("failed to execute Lua script: %w", err)
		}
		var result any
		if len(results) == 1 {
			result, err = luaValueToJSON(results[0], nil, 0)
			if err != nil {
				return nil, fmt.Errorf("failed to capture Lua result: %w", err)
			}
		} else if len(results) > 1 {
			values := make([]any, len(results))
			for index, value := range results {
				values[index], err = luaValueToJSON(value, nil, 0)
				if err != nil {
					return nil, fmt.Errorf("failed to capture Lua result: %w", err)
				}
			}
			result = values
		}
		return &models.LuaOutput{
			Stdout: stdoutBuf.String(),
			Stderr: stderrBuf.String(),
			Result: result,
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.LuaInput, models.LuaOutput]{
		Name:        consts.LuaToolName,
		Description: luaToolDescription(),
		Call:        call,
	}).MustCompile()
}

func luaTableToJSON(table *lua.Table, ancestors []lua.Value, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Lua tool arguments exceed maximum nesting depth")
	}
	tableValue := table.Value()
	for _, ancestor := range ancestors {
		if same, applicable := tableValue.SameObject(ancestor); applicable && same {
			return nil, fmt.Errorf("Lua tool arguments cannot contain cyclic tables")
		}
	}
	ancestors = append(ancestors, tableValue)

	object := make(map[string]any)
	array := make(map[int]any)
	hasStringKeys, hasIntegerKeys := false, false
	previous := lua.Nil()
	for {
		key, value, ok, err := table.Next(previous)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch key.Kind() {
		case lua.StringKind:
			name, _ := key.AsString()
			converted, err := luaValueToJSON(value, ancestors, depth+1)
			if err != nil {
				return nil, err
			}
			hasStringKeys = true
			object[name] = converted
		case lua.NumberKind:
			number, _ := key.AsNumber()
			if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number ||
				number < 1 || number >= float64(int(^uint(0)>>1)) {
				return nil, fmt.Errorf("Lua tool argument table keys must be strings or positive integer indices")
			}
			converted, err := luaValueToJSON(value, ancestors, depth+1)
			if err != nil {
				return nil, err
			}
			hasIntegerKeys = true
			array[int(number)] = converted
		default:
			return nil, fmt.Errorf("Lua tool argument table keys must be strings or positive integer indices")
		}
		previous = key
	}
	if hasStringKeys && hasIntegerKeys {
		return nil, fmt.Errorf("Lua tool argument tables cannot mix object and array keys")
	}
	if !hasIntegerKeys {
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

func luaValueToJSON(value lua.Value, ancestors []lua.Value, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Lua tool arguments exceed maximum nesting depth")
	}
	switch value.Kind() {
	case lua.NilKind:
		return nil, nil
	case lua.BoolKind:
		result, _ := value.AsBool()
		return result, nil
	case lua.NumberKind:
		result, _ := value.AsNumber()
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return nil, fmt.Errorf("Lua tool arguments cannot contain non-finite numbers")
		}
		return result, nil
	case lua.StringKind:
		result, _ := value.AsString()
		return result, nil
	case lua.TableKind:
		table, _ := value.AsTable()
		return luaTableToJSON(table, ancestors, depth)
	default:
		return nil, fmt.Errorf("Lua tool arguments cannot contain %s values", value.Kind())
	}
}
