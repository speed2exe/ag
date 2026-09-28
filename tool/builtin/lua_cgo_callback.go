//go:build cgo

package builtin

/*
#cgo pkg-config: lua
#include <stdint.h>
#include <lua.h>
*/
import "C"

//export ag_go_capture_lua_result
func ag_go_capture_lua_result(state *C.lua_State, handle C.uintptr_t, resultCount C.int) C.int {
	return captureLuaResult(state, uintptr(handle), int(resultCount))
}
