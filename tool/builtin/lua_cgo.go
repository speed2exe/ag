//go:build cgo

package builtin

/*
#cgo pkg-config: lua

#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <lua.h>
#include <lauxlib.h>
#include <lualib.h>

extern int ag_go_capture_lua_result(lua_State *state, uintptr_t handle, int result_count);

static lua_Number ag_lua_tonumber(lua_State *state, int index) {
	return lua_tonumber(state, index);
}

static void ag_lua_pop(lua_State *state, int count) {
	lua_pop(state, count);
}

static char *ag_read_file(FILE *file, size_t *size) {
	*size = 0;
	if (fflush(file) != 0 || fseek(file, 0, SEEK_END) != 0) {
		return NULL;
	}

	long length = ftell(file);
	if (length < 0 || fseek(file, 0, SEEK_SET) != 0) {
		return NULL;
	}

	char *buffer = malloc((size_t)length + 1);
	if (buffer == NULL) {
		return NULL;
	}

	size_t bytes_read = fread(buffer, 1, (size_t)length, file);
	if (bytes_read != (size_t)length || ferror(file)) {
		free(buffer);
		return NULL;
	}
	buffer[bytes_read] = '\0';
	*size = bytes_read;
	return buffer;
}

static char *ag_lua_error_message(lua_State *state, const char *fallback) {
	const char *message = lua_tostring(state, -1);
	return strdup(message != NULL ? message : fallback);
}

static int ag_set_io_stream(lua_State *state, const char *name, FILE *file) {
	lua_getglobal(state, "io");
	lua_getfield(state, -1, name);
	luaL_Stream *stream = luaL_testudata(state, -1, LUA_FILEHANDLE);
	if (stream != NULL) {
		stream->f = file;
	}
	lua_pop(state, 2);
	return stream != NULL;
}

static int ag_redirect_print(lua_State *state) {
	return luaL_dostring(state,
		"function print(...) "
		"  local values = table.pack(...) "
		"  for i = 1, values.n do "
		"    if i > 1 then io.write('\\t') end "
		"    io.write(tostring(values[i])) "
		"  end "
		"  io.write('\\n') "
		"end"
	);
}

static int ag_lua_execute(
	const char *name,
	const char *script,
	size_t script_size,
	uintptr_t result_handle,
	char **stdout_data,
	size_t *stdout_size,
	char **stderr_data,
	size_t *stderr_size,
	char **error_message
) {
	lua_State *state = NULL;
	FILE *stdout_file = NULL;
	FILE *stderr_file = NULL;
	int result = 1;
	int status;

	*stdout_data = NULL;
	*stdout_size = 0;
	*stderr_data = NULL;
	*stderr_size = 0;
	*error_message = NULL;

	state = luaL_newstate();
	if (state == NULL) {
		*error_message = strdup("failed to create Lua state");
		goto cleanup;
	}

	stdout_file = tmpfile();
	stderr_file = tmpfile();
	if (stdout_file == NULL || stderr_file == NULL) {
		*error_message = strdup("failed to create Lua output buffers");
		goto cleanup;
	}

	luaL_openlibs(state);
	if (!ag_set_io_stream(state, "stdout", stdout_file) ||
		!ag_set_io_stream(state, "stderr", stderr_file)) {
		*error_message = strdup("failed to redirect Lua output");
		goto cleanup;
	}
	if (ag_redirect_print(state) != LUA_OK) {
		*error_message = ag_lua_error_message(state, "failed to redirect Lua output");
		goto cleanup;
	}

	status = luaL_loadbufferx(state, script, script_size, name, NULL);
	if (status == LUA_OK) {
		status = lua_pcall(state, 0, LUA_MULTRET, 0);
	}
	if (status != LUA_OK) {
		*error_message = ag_lua_error_message(state, "unknown Lua error");
		goto cleanup;
	}
	if (ag_go_capture_lua_result(state, result_handle, lua_gettop(state)) != 0) {
		*error_message = ag_lua_error_message(state, "failed to capture Lua result");
		goto cleanup;
	}

	*stdout_data = ag_read_file(stdout_file, stdout_size);
	*stderr_data = ag_read_file(stderr_file, stderr_size);
	if (*stdout_data == NULL || *stderr_data == NULL) {
		free(*stdout_data);
		free(*stderr_data);
		*stdout_data = NULL;
		*stderr_data = NULL;
		*error_message = strdup("failed to read Lua output buffers");
		goto cleanup;
	}

	result = 0;

cleanup:
	if (state != NULL) {
		lua_close(state);
	}
	if (stdout_file != NULL && fclose(stdout_file) != 0 && result == 0) {
		*error_message = strdup("failed to close Lua stdout buffer");
		result = 1;
	}
	if (stderr_file != NULL && fclose(stderr_file) != 0 && result == 0) {
		*error_message = strdup("failed to close Lua stderr buffer");
		result = 1;
	}
	if (result != 0) {
		free(*stdout_data);
		free(*stderr_data);
		*stdout_data = NULL;
		*stderr_data = NULL;
	}
	return result;
}
*/
import "C"

import (
	"context"
	"fmt"
	"math"
	"runtime/cgo"
	"unsafe"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func Lua() *tool.Tool {
	call := func(_ context.Context, input *models.LuaInput) (*models.LuaOutput, error) {
		if err := validateLuaInput(input); err != nil {
			return nil, err
		}

		execution := &luaExecution{}
		handle := cgo.NewHandle(execution)
		defer handle.Delete()

		name := C.CString(input.Name)
		defer C.free(unsafe.Pointer(name))
		script := C.CBytes([]byte(input.Script))
		defer C.free(script)

		var stdout, stderr, message *C.char
		var stdoutSize, stderrSize C.size_t
		if C.ag_lua_execute(name,
			(*C.char)(script), C.size_t(len(input.Script)),
			C.uintptr_t(handle),
			&stdout, &stdoutSize,
			&stderr, &stderrSize,
			&message,
		) != 0 {
			defer C.free(unsafe.Pointer(message))
			messageText := "unknown Lua error"
			if message != nil {
				messageText = C.GoString(message)
			}
			return nil, fmt.Errorf("failed to execute Lua script: %s", messageText)
		}
		defer C.free(unsafe.Pointer(stdout))
		defer C.free(unsafe.Pointer(stderr))

		return &models.LuaOutput{
			Stdout: C.GoStringN(stdout, C.int(stdoutSize)),
			Stderr: C.GoStringN(stderr, C.int(stderrSize)),
			Result: execution.result,
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.LuaInput, models.LuaOutput]{
		Name:        consts.LuaToolName,
		Description: luaToolDescription(),
		Call:        call,
	}).MustCompile()
}

type luaExecution struct {
	result any
}

func captureLuaResult(state *C.lua_State, handle uintptr, resultCount int) C.int {
	execution, ok := cgo.Handle(handle).Value().(*luaExecution)
	if !ok {
		pushLuaError(state, "invalid Lua result callback")
		return -1
	}
	if resultCount == 0 {
		return 0
	}

	values := make([]any, int(resultCount))
	for index := range values {
		result, err := readLuaJSONValue(state, C.int(index-int(resultCount)), make(map[unsafe.Pointer]bool), 0)
		if err != nil {
			pushLuaError(state, fmt.Sprintf("failed to capture Lua result: %v", err))
			return -1
		}
		values[index] = result
	}
	if len(values) == 1 {
		execution.result = values[0]
	} else {
		execution.result = values
	}
	return 0
}

func pushLuaError(state *C.lua_State, message string) {
	text := C.CBytes([]byte(message))
	defer C.free(text)
	C.lua_pushlstring(state, (*C.char)(text), C.size_t(len(message)))
}

func readLuaJSONValue(
	state *C.lua_State,
	index C.int,
	seen map[unsafe.Pointer]bool,
	depth int,
) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Lua tool arguments exceed maximum nesting depth")
	}
	switch C.lua_type(state, index) {
	case C.LUA_TNIL:
		return nil, nil
	case C.LUA_TBOOLEAN:
		return C.lua_toboolean(state, index) != 0, nil
	case C.LUA_TNUMBER:
		number := float64(C.ag_lua_tonumber(state, index))
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("Lua tool arguments cannot contain non-finite numbers")
		}
		return number, nil
	case C.LUA_TSTRING:
		var length C.size_t
		text := C.lua_tolstring(state, index, &length)
		return C.GoStringN(text, C.int(length)), nil
	case C.LUA_TTABLE:
		absoluteIndex := C.lua_absindex(state, index)
		pointer := C.lua_topointer(state, absoluteIndex)
		if seen[pointer] {
			return nil, fmt.Errorf("Lua tool arguments cannot contain cyclic tables")
		}
		seen[pointer] = true
		defer delete(seen, pointer)

		object := make(map[string]any)
		array := make(map[int]any)
		hasStringKeys, hasIntegerKeys := false, false
		C.lua_pushnil(state)
		for C.lua_next(state, absoluteIndex) != 0 {
			switch C.lua_type(state, -2) {
			case C.LUA_TSTRING:
				hasStringKeys = true
				var length C.size_t
				key := C.lua_tolstring(state, -2, &length)
				value, err := readLuaJSONValue(state, -1, seen, depth+1)
				if err != nil {
					C.ag_lua_pop(state, 2)
					return nil, err
				}
				object[C.GoStringN(key, C.int(length))] = value
			case C.LUA_TNUMBER:
				number := float64(C.ag_lua_tonumber(state, -2))
				if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number ||
					number < 1 || number >= float64(int(^uint(0)>>1)) {
					C.ag_lua_pop(state, 2)
					return nil, fmt.Errorf("Lua tool argument table keys must be strings or positive integer indices")
				}
				hasIntegerKeys = true
				value, err := readLuaJSONValue(state, -1, seen, depth+1)
				if err != nil {
					C.ag_lua_pop(state, 2)
					return nil, err
				}
				array[int(number)] = value
			default:
				C.ag_lua_pop(state, 2)
				return nil, fmt.Errorf("Lua tool argument table keys must be strings or positive integer indices")
			}
			C.ag_lua_pop(state, 1)
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
	default:
		return nil, fmt.Errorf("Lua tool arguments cannot contain %s values", C.GoString(C.lua_typename(state, C.lua_type(state, index))))
	}
}
