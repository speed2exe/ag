package a2a

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	a2acore "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

const (
	ToolPrefix                   = "a2a_"
	SendMessageToolName          = "a2a_send_message"
	SendStreamingMessageToolName = "a2a_send_streaming_message"
	GetTaskToolName              = "a2a_get_task"
	ListTasksToolName            = "a2a_list_tasks"
	CancelTaskToolName           = "a2a_cancel_task"
	SubscribeToTaskToolName      = "a2a_subscribe_to_task"
	GetTaskPushConfigToolName    = "a2a_get_task_push_config"
	ListTaskPushConfigsToolName  = "a2a_list_task_push_configs"
	CreateTaskPushConfigToolName = "a2a_create_task_push_config"
	DeleteTaskPushConfigToolName = "a2a_delete_task_push_config"
	GetExtendedAgentCardToolName = "a2a_get_extended_agent_card"
)

// Tools returns all A2A client protocol methods as tools when at least one agent is configured.
func (c *Clients) Tools() []*tool.Tool {
	if c == nil || len(c.agents) == 0 {
		return nil
	}
	return []*tool.Tool{
		c.sendMessageTool(),
		c.sendStreamingMessageTool(),
		c.getTaskTool(),
		c.listTasksTool(),
		c.cancelTaskTool(),
		c.subscribeToTaskTool(),
		c.getTaskPushConfigTool(),
		c.listTaskPushConfigsTool(),
		c.createTaskPushConfigTool(),
		c.deleteTaskPushConfigTool(),
		c.getExtendedAgentCardTool(),
	}
}

type PartInput struct {
	Text      string         `json:"text,omitempty" jsonschema:"Plain text content of the part."`
	URL       string         `json:"url,omitempty" jsonschema:"URL referencing a file or resource."`
	RawBase64 string         `json:"raw,omitempty" jsonschema:"Base64-encoded raw bytes content of the part."`
	Data      any            `json:"data,omitempty" jsonschema:"Structured JSON data content of the part."`
	Filename  string         `json:"filename,omitempty" jsonschema:"Optional filename (e.g., document.pdf)."`
	MediaType string         `json:"media_type,omitempty" jsonschema:"Optional MIME media type (e.g., text/plain, image/png, application/json)."`
	Metadata  map[string]any `json:"metadata,omitempty" jsonschema:"Optional extension metadata for this part."`
}

type PushAuthInput struct {
	Scheme      string `json:"scheme" jsonschema:"Authentication scheme (e.g., Bearer, Basic)."`
	Credentials string `json:"credentials,omitempty" jsonschema:"Optional credentials for the push notification endpoint."`
}

type PushConfigInput struct {
	ID     string         `json:"id,omitempty" jsonschema:"Optional unique ID for the push notification configuration."`
	TaskID string         `json:"task_id,omitempty" jsonschema:"Optional task ID associated with the push configuration."`
	Tenant string         `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
	URL    string         `json:"url" jsonschema:"Callback URL where the agent should send push notifications."`
	Token  string         `json:"token,omitempty" jsonschema:"Optional token to validate incoming push notifications."`
	Auth   *PushAuthInput `json:"authentication,omitempty" jsonschema:"Optional authentication details for the callback URL."`
}

type SendMessageConfigInput struct {
	AcceptedOutputModes []string         `json:"accepted_output_modes,omitempty" jsonschema:"Output MIME types the client accepts in the response."`
	ReturnImmediately   bool             `json:"return_immediately,omitempty" jsonschema:"If true, return immediately after creating the task without waiting for completion."`
	HistoryLength       *int             `json:"history_length,omitempty" jsonschema:"Number of most recent messages from task history to include in the response."`
	PushConfig          *PushConfigInput `json:"push_config,omitempty" jsonschema:"Push notification configuration for asynchronous task updates."`
}

type SendMessageInput struct {
	AgentURL       string                  `json:"agent_url" jsonschema:"URL of the A2A agent (from the agent cards listed in instructions)."`
	Text           string                  `json:"text,omitempty" jsonschema:"Convenience plain text message content. Either text or parts must be provided."`
	Parts          []PartInput             `json:"parts,omitempty" jsonschema:"Structured content parts for the message (each setting exactly one of text, url, raw, or data)."`
	Role           string                  `json:"role,omitempty" jsonschema:"Message role: ROLE_USER (default) or ROLE_AGENT."`
	MessageID      string                  `json:"message_id,omitempty" jsonschema:"Optional unique message ID; auto-generated if omitted."`
	TaskID         string                  `json:"task_id,omitempty" jsonschema:"Optional existing task ID to continue."`
	ContextID      string                  `json:"context_id,omitempty" jsonschema:"Optional context ID to group related interactions."`
	ReferenceTasks []string                `json:"reference_task_ids,omitempty" jsonschema:"Optional list of other task IDs referenced for context."`
	Extensions     []string                `json:"extensions,omitempty" jsonschema:"Optional URIs of relevant protocol extensions."`
	MessageMeta    map[string]any          `json:"message_metadata,omitempty" jsonschema:"Optional extension metadata for the message."`
	Metadata       map[string]any          `json:"metadata,omitempty" jsonschema:"Optional extension metadata for the request."`
	Tenant         string                  `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
	Config         *SendMessageConfigInput `json:"configuration,omitempty" jsonschema:"Optional configuration for the send request."`
}

type StreamEventsOutput struct {
	Events []a2acore.StreamResponse `json:"events"`
}

func (c *Clients) sendMessageTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[SendMessageInput, a2acore.StreamResponse]{
		Name:        SendMessageToolName,
		Description: new("Send a message to an A2A agent (non-streaming). Creates a new task, continues an existing task when task_id is set, or returns a direct message response."),
		Call: func(ctx context.Context, input *SendMessageInput) (*a2acore.StreamResponse, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			req, err := buildSendMessageRequest(input)
			if err != nil {
				return nil, err
			}
			res, err := client.SendMessage(ctx, req)
			if err != nil {
				return nil, err
			}
			return &a2acore.StreamResponse{Event: res}, nil
		},
	}).MustCompile()
}

func (c *Clients) sendStreamingMessageTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[SendMessageInput, StreamEventsOutput]{
		Name:        SendStreamingMessageToolName,
		Description: new("Send a message to an A2A agent using streaming and collect all streamed events until the stream completes. Automatically falls back to non-streaming if the agent does not support streaming."),
		Call: func(ctx context.Context, input *SendMessageInput) (*StreamEventsOutput, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			req, err := buildSendMessageRequest(input)
			if err != nil {
				return nil, err
			}
			events := make([]a2acore.StreamResponse, 0)
			for ev, err := range client.SendStreamingMessage(ctx, req) {
				if err != nil {
					return nil, err
				}
				events = append(events, a2acore.StreamResponse{Event: ev})
			}
			return &StreamEventsOutput{Events: events}, nil
		},
	}).MustCompile()
}

type GetTaskInput struct {
	AgentURL      string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	ID            string `json:"id" jsonschema:"ID of the task to retrieve."`
	HistoryLength *int   `json:"history_length,omitempty" jsonschema:"Number of most recent messages from the task's history to retrieve."`
	Tenant        string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) getTaskTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[GetTaskInput, a2acore.Task]{
		Name:        GetTaskToolName,
		Description: new("Get the current state, status, artifacts, and optional history of a task from an A2A agent."),
		Call: func(ctx context.Context, input *GetTaskInput) (*a2acore.Task, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			return client.GetTask(ctx, &a2acore.GetTaskRequest{
				Tenant:        input.Tenant,
				ID:            a2acore.TaskID(input.ID),
				HistoryLength: input.HistoryLength,
			})
		},
	}).MustCompile()
}

type ListTasksInput struct {
	AgentURL             string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	ContextID            string `json:"context_id,omitempty" jsonschema:"Filter tasks by context ID."`
	Status               string `json:"status,omitempty" jsonschema:"Filter tasks by state (e.g., TASK_STATE_SUBMITTED, TASK_STATE_WORKING, TASK_STATE_COMPLETED, TASK_STATE_FAILED, TASK_STATE_CANCELED, TASK_STATE_INPUT_REQUIRED, TASK_STATE_REJECTED, TASK_STATE_AUTH_REQUIRED)."`
	PageSize             int    `json:"page_size,omitempty" jsonschema:"Maximum number of tasks to return (1-100, default 50)."`
	PageToken            string `json:"page_token,omitempty" jsonschema:"Pagination token from a previous ListTasks response."`
	HistoryLength        *int   `json:"history_length,omitempty" jsonschema:"Number of most recent messages from each task's history to retrieve."`
	StatusTimestampAfter string `json:"status_timestamp_after,omitempty" jsonschema:"Optional RFC3339 timestamp to list tasks updated after this time."`
	IncludeArtifacts     bool   `json:"include_artifacts,omitempty" jsonschema:"Whether to include artifacts in the returned tasks."`
	Tenant               string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) listTasksTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[ListTasksInput, a2acore.ListTasksResponse]{
		Name:        ListTasksToolName,
		Description: new("List tasks from an A2A agent with optional filtering and pagination."),
		Call: func(ctx context.Context, input *ListTasksInput) (*a2acore.ListTasksResponse, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			status, err := parseTaskState(input.Status)
			if err != nil {
				return nil, err
			}
			var statusAfter *time.Time
			if input.StatusTimestampAfter != "" {
				t, err := time.Parse(time.RFC3339Nano, input.StatusTimestampAfter)
				if err != nil {
					return nil, fmt.Errorf("invalid status_timestamp_after %q (expected RFC3339): %w", input.StatusTimestampAfter, err)
				}
				statusAfter = &t
			}
			return client.ListTasks(ctx, &a2acore.ListTasksRequest{
				Tenant:               input.Tenant,
				ContextID:            input.ContextID,
				Status:               status,
				PageSize:             input.PageSize,
				PageToken:            input.PageToken,
				HistoryLength:        input.HistoryLength,
				StatusTimestampAfter: statusAfter,
				IncludeArtifacts:     input.IncludeArtifacts,
			})
		},
	}).MustCompile()
}

type CancelTaskInput struct {
	AgentURL string         `json:"agent_url" jsonschema:"URL of the A2A agent."`
	ID       string         `json:"id" jsonschema:"ID of the task to cancel."`
	Metadata map[string]any `json:"metadata,omitempty" jsonschema:"Optional extension metadata."`
	Tenant   string         `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) cancelTaskTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[CancelTaskInput, a2acore.Task]{
		Name:        CancelTaskToolName,
		Description: new("Request cancellation of an ongoing task on an A2A agent."),
		Call: func(ctx context.Context, input *CancelTaskInput) (*a2acore.Task, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			return client.CancelTask(ctx, &a2acore.CancelTaskRequest{
				Tenant:   input.Tenant,
				ID:       a2acore.TaskID(input.ID),
				Metadata: input.Metadata,
			})
		},
	}).MustCompile()
}

type SubscribeToTaskInput struct {
	AgentURL string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	ID       string `json:"id" jsonschema:"ID of the task to subscribe to."`
	Tenant   string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) subscribeToTaskTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[SubscribeToTaskInput, StreamEventsOutput]{
		Name:        SubscribeToTaskToolName,
		Description: new("Subscribe to streaming updates for an existing task on an A2A agent and collect events until the stream finishes."),
		Call: func(ctx context.Context, input *SubscribeToTaskInput) (*StreamEventsOutput, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			events := make([]a2acore.StreamResponse, 0)
			for ev, err := range client.SubscribeToTask(ctx, &a2acore.SubscribeToTaskRequest{
				Tenant: input.Tenant,
				ID:     a2acore.TaskID(input.ID),
			}) {
				if err != nil {
					return nil, err
				}
				events = append(events, a2acore.StreamResponse{Event: ev})
			}
			return &StreamEventsOutput{Events: events}, nil
		},
	}).MustCompile()
}

type GetTaskPushConfigInput struct {
	AgentURL string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	TaskID   string `json:"task_id" jsonschema:"ID of the parent task."`
	ID       string `json:"id" jsonschema:"ID of the push notification configuration to retrieve."`
	Tenant   string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) getTaskPushConfigTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[GetTaskPushConfigInput, a2acore.PushConfig]{
		Name:        GetTaskPushConfigToolName,
		Description: new("Get a specific push notification configuration for a task on an A2A agent."),
		Call: func(ctx context.Context, input *GetTaskPushConfigInput) (*a2acore.PushConfig, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			return client.GetTaskPushConfig(ctx, &a2acore.GetTaskPushConfigRequest{
				Tenant: input.Tenant,
				TaskID: a2acore.TaskID(input.TaskID),
				ID:     input.ID,
			})
		},
	}).MustCompile()
}

type ListTaskPushConfigsInput struct {
	AgentURL  string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	TaskID    string `json:"task_id" jsonschema:"ID of the task."`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"Maximum number of push notification configurations to return."`
	PageToken string `json:"page_token,omitempty" jsonschema:"Pagination token from a previous response."`
	Tenant    string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

type ListTaskPushConfigsOutput struct {
	Configs []*a2acore.PushConfig `json:"configs"`
}

func (c *Clients) listTaskPushConfigsTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[ListTaskPushConfigsInput, ListTaskPushConfigsOutput]{
		Name:        ListTaskPushConfigsToolName,
		Description: new("List push notification configurations for a task on an A2A agent."),
		Call: func(ctx context.Context, input *ListTaskPushConfigsInput) (*ListTaskPushConfigsOutput, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			configs, err := client.ListTaskPushConfigs(ctx, &a2acore.ListTaskPushConfigRequest{
				Tenant:    input.Tenant,
				TaskID:    a2acore.TaskID(input.TaskID),
				PageSize:  input.PageSize,
				PageToken: input.PageToken,
			})
			if err != nil {
				return nil, err
			}
			if configs == nil {
				configs = []*a2acore.PushConfig{}
			}
			return &ListTaskPushConfigsOutput{Configs: configs}, nil
		},
	}).MustCompile()
}

type CreateTaskPushConfigInput struct {
	AgentURL string         `json:"agent_url" jsonschema:"URL of the A2A agent."`
	TaskID   string         `json:"task_id" jsonschema:"ID of the task."`
	ID       string         `json:"id,omitempty" jsonschema:"Optional unique ID for the push notification configuration."`
	URL      string         `json:"url" jsonschema:"Callback URL where the agent should send push notifications."`
	Token    string         `json:"token,omitempty" jsonschema:"Optional token to validate incoming push notifications."`
	Auth     *PushAuthInput `json:"authentication,omitempty" jsonschema:"Optional authentication details for the callback URL."`
	Tenant   string         `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) createTaskPushConfigTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[CreateTaskPushConfigInput, a2acore.PushConfig]{
		Name:        CreateTaskPushConfigToolName,
		Description: new("Create a push notification configuration for a task on an A2A agent."),
		Call: func(ctx context.Context, input *CreateTaskPushConfigInput) (*a2acore.PushConfig, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			req := &a2acore.PushConfig{
				Tenant: input.Tenant,
				TaskID: a2acore.TaskID(input.TaskID),
				ID:     input.ID,
				URL:    input.URL,
				Token:  input.Token,
			}
			if input.Auth != nil {
				req.Auth = &a2acore.PushAuthInfo{
					Scheme:      input.Auth.Scheme,
					Credentials: input.Auth.Credentials,
				}
			}
			return client.CreateTaskPushConfig(ctx, req)
		},
	}).MustCompile()
}

type DeleteTaskPushConfigInput struct {
	AgentURL string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	TaskID   string `json:"task_id" jsonschema:"ID of the parent task."`
	ID       string `json:"id" jsonschema:"ID of the push notification configuration to delete."`
	Tenant   string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

type DeleteTaskPushConfigOutput struct {
	Deleted bool `json:"deleted"`
}

func (c *Clients) deleteTaskPushConfigTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[DeleteTaskPushConfigInput, DeleteTaskPushConfigOutput]{
		Name:        DeleteTaskPushConfigToolName,
		Description: new("Delete a push notification configuration for a task on an A2A agent."),
		Call: func(ctx context.Context, input *DeleteTaskPushConfigInput) (*DeleteTaskPushConfigOutput, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			err = client.DeleteTaskPushConfig(ctx, &a2acore.DeleteTaskPushConfigRequest{
				Tenant: input.Tenant,
				TaskID: a2acore.TaskID(input.TaskID),
				ID:     input.ID,
			})
			if err != nil {
				return nil, err
			}
			return &DeleteTaskPushConfigOutput{Deleted: true}, nil
		},
	}).MustCompile()
}

type GetExtendedAgentCardInput struct {
	AgentURL string `json:"agent_url" jsonschema:"URL of the A2A agent."`
	Tenant   string `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
}

func (c *Clients) getExtendedAgentCardTool() *tool.Tool {
	return (&util.ToolForJsonInputOutput[GetExtendedAgentCardInput, a2acore.AgentCard]{
		Name:        GetExtendedAgentCardToolName,
		Description: new("Fetch the authenticated extended agent card from an A2A agent (when supported by the agent)."),
		Call: func(ctx context.Context, input *GetExtendedAgentCardInput) (*a2acore.AgentCard, error) {
			client, err := c.resolveClient(input.AgentURL)
			if err != nil {
				return nil, err
			}
			return client.GetExtendedAgentCard(ctx, &a2acore.GetExtendedAgentCardRequest{
				Tenant: input.Tenant,
			})
		},
	}).MustCompile()
}

func buildSendMessageRequest(input *SendMessageInput) (*a2acore.SendMessageRequest, error) {
	role, err := parseMessageRole(input.Role)
	if err != nil {
		return nil, err
	}

	parts := make(a2acore.ContentParts, 0, len(input.Parts)+1)
	if input.Text != "" {
		parts = append(parts, a2acore.NewTextPart(input.Text))
	}
	for i, p := range input.Parts {
		part, err := convertPartInput(p)
		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return nil, errors.New("either text or parts must be provided")
	}

	msgID := input.MessageID
	if msgID == "" {
		msgID = a2acore.NewMessageID()
	}

	var refTasks []a2acore.TaskID
	if len(input.ReferenceTasks) > 0 {
		refTasks = make([]a2acore.TaskID, len(input.ReferenceTasks))
		for i, id := range input.ReferenceTasks {
			refTasks[i] = a2acore.TaskID(id)
		}
	}

	msg := &a2acore.Message{
		ID:             msgID,
		Role:           role,
		Parts:          parts,
		TaskID:         a2acore.TaskID(input.TaskID),
		ContextID:      input.ContextID,
		ReferenceTasks: refTasks,
		Extensions:     input.Extensions,
		Metadata:       input.MessageMeta,
	}

	req := &a2acore.SendMessageRequest{
		Tenant:   input.Tenant,
		Message:  msg,
		Metadata: input.Metadata,
	}

	if input.Config != nil {
		cfg := &a2acore.SendMessageConfig{
			AcceptedOutputModes: input.Config.AcceptedOutputModes,
			ReturnImmediately:   input.Config.ReturnImmediately,
			HistoryLength:       input.Config.HistoryLength,
		}
		if input.Config.PushConfig != nil {
			pc := input.Config.PushConfig
			cfg.PushConfig = &a2acore.PushConfig{
				Tenant: pc.Tenant,
				TaskID: a2acore.TaskID(pc.TaskID),
				ID:     pc.ID,
				URL:    pc.URL,
				Token:  pc.Token,
			}
			if pc.Auth != nil {
				cfg.PushConfig.Auth = &a2acore.PushAuthInfo{
					Scheme:      pc.Auth.Scheme,
					Credentials: pc.Auth.Credentials,
				}
			}
		}
		req.Config = cfg
	}

	return req, nil
}

func convertPartInput(p PartInput) (*a2acore.Part, error) {
	var content a2acore.PartContent
	var setCount int

	if p.Text != "" {
		content = a2acore.Text(p.Text)
		setCount++
	}
	if p.URL != "" {
		content = a2acore.URL(p.URL)
		setCount++
	}
	if p.RawBase64 != "" {
		raw, err := decodeBase64(p.RawBase64)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 in raw: %w", err)
		}
		content = a2acore.Raw(raw)
		setCount++
	}
	if p.Data != nil {
		content = a2acore.Data{Value: p.Data}
		setCount++
	}

	if setCount == 0 {
		return nil, errors.New("part must specify exactly one of text, url, raw, or data")
	}
	if setCount > 1 {
		return nil, fmt.Errorf("part must specify only one of text, url, raw, or data (got %d)", setCount)
	}

	return &a2acore.Part{
		Content:   content,
		Filename:  p.Filename,
		MediaType: p.MediaType,
		Metadata:  p.Metadata,
	}, nil
}

func decodeBase64(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(s)
	}
	return raw, err
}

func parseMessageRole(role string) (a2acore.MessageRole, error) {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "", "USER", string(a2acore.MessageRoleUser):
		return a2acore.MessageRoleUser, nil
	case "AGENT", string(a2acore.MessageRoleAgent):
		return a2acore.MessageRoleAgent, nil
	default:
		return a2acore.MessageRoleUnspecified, fmt.Errorf("invalid message role %q: expected ROLE_USER or ROLE_AGENT", role)
	}
}

func parseTaskState(status string) (a2acore.TaskState, error) {
	trimmed := strings.TrimSpace(status)
	if trimmed == "" {
		return a2acore.TaskStateUnspecified, nil
	}
	normalized := strings.ToUpper(strings.ReplaceAll(trimmed, "-", "_"))
	if !strings.HasPrefix(normalized, "TASK_STATE_") {
		if normalized == "CANCELLED" {
			normalized = "CANCELED"
		}
		normalized = "TASK_STATE_" + normalized
	}
	state := a2acore.TaskStateFromString(normalized)
	if state == a2acore.TaskStateUnspecified {
		return a2acore.TaskStateUnspecified, fmt.Errorf("invalid task status %q", status)
	}
	return state, nil
}
