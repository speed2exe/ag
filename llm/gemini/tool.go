package gemini

import "context"

// CallTool implements [llm.ToolCaller].
func (c *Client) CallTool(ctx context.Context, toolName, args string) (string, bool, error) {
	return "", false, nil
}
