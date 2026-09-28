package tool

import "context"

type Tool struct {
	Name            string
	Description     *string
	InputSchema     []byte
	LuaOutputAsText bool
	Call            func(ctx context.Context, args []byte) ([]byte, error)
}
