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

func Lua(tools []*tool.Tool) *tool.Tool {
	tools = append([]*tool.Tool(nil), tools...)
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
		defer s.Close()

		if err := validateLuaTools(tools); err != nil {
			return nil, err
		}
		for _, currentTool := range tools {
			toolFunction, err := s.NewNativeFunction(func(frame lua.Frame) lua.Outcome {
				if frame.ArgumentCount() != 1 {
					frame.ThrowString("tool expects one table argument")
				}
				arguments, ok := frame.Table(0)
				if !ok {
					frame.ThrowArgTypeError(0, lua.TableKind)
				}
				argumentValue, err := luaTableToJSON(arguments, nil, 0)
				if err != nil {
					frame.ThrowError(err)
				}
				result, err := callLuaTool(ctx, currentTool, argumentValue)
				if err != nil {
					frame.ThrowError(err)
				}
				resultValue, err := luaJSONToValue(frame.State(), result)
				if err != nil {
					frame.ThrowError(fmt.Errorf("failed to decode tool result: %w", err))
				}
				return frame.ReturnValue(resultValue)
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create Lua function for tool %s: %w", currentTool.Name, err)
			}
			if err := s.RawSetGlobal(currentTool.Name, toolFunction.Value()); err != nil {
				return nil, fmt.Errorf("failed to register Lua tool %s: %w", currentTool.Name, err)
			}
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
		Description: luaToolDescription(tools),
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

func luaJSONToValue(state *lua.State, value any) (lua.Value, error) {
	switch value := value.(type) {
	case nil:
		return lua.Nil(), nil
	case bool:
		return lua.Bool(value), nil
	case float64:
		return lua.Number(value), nil
	case int64:
		return lua.Number(float64(value)), nil
	case string:
		return state.String(value), nil
	case []any, map[string]any:
		table, err := state.NewTableFrom(value)
		if err != nil {
			return lua.Value{}, err
		}
		return table.Value(), nil
	default:
		return lua.Value{}, fmt.Errorf("unsupported JSON value type %T", value)
	}
}
