//go:build cgo

package builtin

/*
#cgo pkg-config: lua

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <lua.h>
#include <lauxlib.h>
#include <lualib.h>

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
	"strings"
	"unsafe"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

func Lua() *tool.Tool {
	call := func(ctx context.Context, input *models.LuaInput) (*models.LuaOutput, error) {
		if strings.IndexByte(input.Name, 0) >= 0 {
			return nil, fmt.Errorf("Lua script name contains a NUL byte")
		}

		name := C.CString(input.Name)
		defer C.free(unsafe.Pointer(name))
		script := C.CBytes([]byte(input.Script))
		defer C.free(script)

		var stdout, stderr, message *C.char
		var stdoutSize, stderrSize C.size_t
		if C.ag_lua_execute(name,
			(*C.char)(script), C.size_t(len(input.Script)),
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
		}, nil
	}

	return (&util.ToolForJsonInputOutput[models.LuaInput, models.LuaOutput]{
		Name:        consts.LuaToolName,
		Description: new(`Executes a Lua script. Name: name of the script. Script: Lua code to execute. Returns stdout and stderr.`),
		Call:        call,
	}).MustCompile()
}
