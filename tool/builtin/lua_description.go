package builtin

func luaToolDescription() *string {
	text := "Executes Lua scripts and returns stdout, stderr, and the script's return value; multiple returned values are returned as an array."
	return &text
}
