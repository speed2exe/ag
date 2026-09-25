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
	Return []string `json:"values"`
	Stdout string   `json:"stdout"`
	Stderr string   `json:"stderr"`
}

type Event interface{ isEvent() }

type Output interface{ isOutput() }

type Input interface{ isInput() }

type OutputUnknown struct{ Any any }

func (e *OutputUnknown) isOutput() {}
func (e *OutputUnknown) isEvent()  {}

type OutputText struct{ Text string }

func (e *OutputText) isOutput() {}
func (e *OutputText) isEvent()  {}

type OutputReasoning struct{ Reasoning string }

func (e *OutputReasoning) isOutput() {}
func (e *OutputReasoning) isEvent()  {}

type OutputFnCall struct{ CallId, Name string }

func (e *OutputFnCall) isOutput() {}
func (e *OutputFnCall) isEvent()  {}

type OutputFnArgs struct{ Args string }

func (e *OutputFnArgs) isOutput() {}
func (e *OutputFnArgs) isEvent()  {}

type OutputErr struct{ Err string }

func (e *OutputErr) isOutput()  {}
func (e *OutputErr) isEvent()   {}
func (e *OutputErr) isMessage() {}

type InputUnknown struct{ Any any }

func (i *InputUnknown) isInput() {}
func (i *InputUnknown) isEvent() {}

type TokenUsage struct{ InputTokens, InputTokensCached, OutputTokens, OutputTokensReasoning int }

func (i *TokenUsage) isEvent() {}

type Message interface{ isMessage() }
type UserPrompt struct{ Text string }

func (m *UserPrompt) isMessage() {}

type AgentReasoning struct{ Reasoning string }

func (m *AgentReasoning) isMessage() {}

type AgentResponse struct{ Text string }

func (m *AgentResponse) isMessage() {}

type ToolCall struct{ CallId, Name, Args, Result string }
type ToolCalls struct{ Items []*ToolCall }

func (m *ToolCalls) isMessage() {}
func (m *ToolCalls) isEvent()   {}
func (m *ToolCalls) isInput()   {}

type ContextId struct{ Id string }

func (c *ContextId) isMessage() {}

type Summary struct{ Text string }

func (s *Summary) isMessage() {}

type Log struct{ Text string }

func (l *Log) isEvent() {}

type Spin struct{}

func (s *Spin) isEvent() {}
