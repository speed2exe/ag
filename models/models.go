package models

type BashInput struct {
	Command string `json:"command"`
}

type BashOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

type AgentInput struct {
	ToolNames  []string `json:"tool_names,omitempty"`
	SkillPaths []string `json:"skill_paths,omitempty"`
	Prompts    string   `json:"prompt"`
}

type AgentOutput struct {
	TotalInputTokens           int     `json:"total_input_tokens"`
	TotalInputTokensCached     int     `json:"total_input_tokens_cached"`
	TotalOutputTokens          int     `json:"total_output_tokens"`
	TotalOutputTokensReasoning int     `json:"total_output_tokens_reasoning"`
	LastResponseText           string  `json:"last_response_text"`
	Error                      *string `json:"error,omitempty"`
}

type LuaInput struct {
	Name   string `json:"name"`
	Script string `json:"script"`
}

type LuaOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Result any    `json:"result,omitempty"`
}

type CompactInput struct {
	Instructions string `json:"instructions,omitempty" jsonschema:"Optional guidance on what to prioritize or preserve when compacting the conversation history."`
}

type CompactOutput struct {
	Compacted bool   `json:"compacted"`
	Message   string `json:"message"`
}

type Event interface{ isEvent() }

type Output interface{ isOutput() }

type OutputText struct{ Text string }

func (*OutputText) isOutput() {}
func (*OutputText) isEvent()  {}

type OutputReasoning struct{ Reasoning string }

func (*OutputReasoning) isOutput() {}
func (*OutputReasoning) isEvent()  {}

type OutputFnCall struct{ CallId, Name string }

func (*OutputFnCall) isOutput() {}
func (*OutputFnCall) isEvent()  {}

type OutputFnArgs struct{ Args string }

func (*OutputFnArgs) isOutput() {}
func (*OutputFnArgs) isEvent()  {}

type OutputErr struct{ Err string }

func (*OutputErr) isOutput()  {}
func (*OutputErr) isEvent()   {}
func (*OutputErr) isMessage() {}

type TokenUsage struct{ InputTokens, InputTokensCached, OutputTokens, OutputTokensReasoning int }

func (*TokenUsage) isEvent() {}

type Message interface{ isMessage() }

type UserPrompt struct{ Text string }

func (*UserPrompt) isMessage() {}

type AgentReasoning struct{ Reasoning string }

func (*AgentReasoning) isMessage() {}

type AgentResponse struct{ Text string }

func (*AgentResponse) isMessage() {}

type ToolCall struct{ CallId, Name, Args, Result string }
type ToolCalls struct{ Items []*ToolCall }

func (*ToolCalls) isMessage() {}
func (*ToolCalls) isEvent()   {}

type ContextId struct{ Id string }

func (*ContextId) isMessage() {}

type Summary struct{ Text string }

func (*Summary) isMessage() {}

type Log struct{ Text string }

func (*Log) isEvent() {}

type Spin struct{}

func (*Spin) isEvent() {}

type Compacted struct{ Messages []Message }

func (*Compacted) isEvent() {}
