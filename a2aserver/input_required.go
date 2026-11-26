package a2aserver

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

type inputRequestKey struct{}

type inputRequest struct {
	mu        sync.Mutex
	requested bool
	prompt    string
	metadata  map[string]any
}

func withInputRequest(ctx context.Context, req *inputRequest) context.Context {
	return context.WithValue(ctx, inputRequestKey{}, req)
}

func inputRequestFrom(ctx context.Context) (*inputRequest, bool) {
	req, ok := ctx.Value(inputRequestKey{}).(*inputRequest)
	return req, ok && req != nil
}

func (r *inputRequest) set(prompt string, metadata map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requested = true
	r.prompt = prompt
	if len(metadata) > 0 {
		r.metadata = maps.Clone(metadata)
	} else {
		r.metadata = nil
	}
}

func (r *inputRequest) get() (bool, string, map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requested, r.prompt, maps.Clone(r.metadata)
}

// RequestInputTool returns the built-in A2A server tool that lets the agent
// explicitly transition the active A2A task into TASK_STATE_INPUT_REQUIRED
// when additional user input or clarification is needed to continue.
func RequestInputTool() *tool.Tool {
	return util.NewJSONTool(
		consts.A2ARequestInputToolName,
		"Request follow-up input from the A2A client and pause the current task in TASK_STATE_INPUT_REQUIRED state. "+
			"Use this when you cannot complete the request without clarification, confirmation, or missing information from the caller. "+
			"After calling this tool, stop calling additional tools and present the question clearly.",
		func(ctx context.Context, in *models.A2ARequestInputInput) (*models.A2ARequestInputOutput, error) {
			prompt := strings.TrimSpace(in.Prompt)
			if prompt == "" {
				return nil, errors.New("prompt is required")
			}
			req, ok := inputRequestFrom(ctx)
			if !ok {
				return &models.A2ARequestInputOutput{
					Requested: false,
					Message:   "not running inside an A2A task execution",
				}, nil
			}
			req.set(prompt, in.Metadata)
			return &models.A2ARequestInputOutput{
				Requested: true,
				Message:   "Input request recorded. Do not call more tools; conclude your response concisely with the question for the caller so the task transitions to input-required.",
			}, nil
		},
	)
}

const a2aSystemInstruction = `You are serving requests over the A2A (Agent2Agent) protocol.
- Provide clear, self-contained responses to the caller's request.
- If you need clarification, confirmation, or missing information from the caller before you can complete the task, call the ` + consts.A2ARequestInputToolName + ` tool with the exact question, then end your turn without calling further tools so the task transitions to input-required state and the caller can reply on the same task.`
