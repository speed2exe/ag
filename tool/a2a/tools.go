package a2a

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"iter"
	"strings"

	a2acore "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

const ToolPrefix = "a2a_"

type agentRef struct {
	AgentURL string `json:"agent_url" jsonschema:"URL of the A2A agent (from the agent cards listed in instructions)."`
}

func (r agentRef) agentURL() string { return r.AgentURL }

// listTasksReq strips a2acore.ListTasksRequest's MarshalJSON method so encoding/json/v2 allows embedding it.
type listTasksReq a2acore.ListTasksRequest

type partInput struct {
	Text      string         `json:"text,omitempty" jsonschema:"Plain text content."`
	URL       string         `json:"url,omitempty" jsonschema:"URL referencing a file or resource."`
	Raw       string         `json:"raw,omitempty" jsonschema:"Base64-encoded raw bytes content."`
	Data      any            `json:"data,omitempty" jsonschema:"Structured JSON data content."`
	Filename  string         `json:"filename,omitempty" jsonschema:"Optional filename."`
	MediaType string         `json:"mediaType,omitempty" jsonschema:"Optional MIME media type (e.g., text/plain, image/png, application/json)."`
	Metadata  map[string]any `json:"metadata,omitempty" jsonschema:"Optional extension metadata."`
}

type sendMessageInput struct {
	agentRef
	Text           string                     `json:"text,omitempty" jsonschema:"Plain text message content. Either text or parts must be provided."`
	Parts          []partInput                `json:"parts,omitempty" jsonschema:"Structured content parts (each setting exactly one of text, url, raw, or data)."`
	Role           a2acore.MessageRole        `json:"role,omitempty" jsonschema:"Message role: ROLE_USER (default) or ROLE_AGENT."`
	MessageID      string                     `json:"messageId,omitempty" jsonschema:"Optional unique message ID; auto-generated if omitted."`
	TaskID         a2acore.TaskID             `json:"taskId,omitempty" jsonschema:"Optional existing task ID to continue."`
	ContextID      string                     `json:"contextId,omitempty" jsonschema:"Optional context ID to group related interactions."`
	ReferenceTasks []a2acore.TaskID           `json:"referenceTaskIds,omitempty" jsonschema:"Optional list of referenced task IDs."`
	Extensions     []string                   `json:"extensions,omitempty" jsonschema:"Optional URIs of relevant protocol extensions."`
	MessageMeta    map[string]any             `json:"messageMetadata,omitempty" jsonschema:"Optional extension metadata for the message."`
	Metadata       map[string]any             `json:"metadata,omitempty" jsonschema:"Optional extension metadata for the request."`
	Tenant         string                     `json:"tenant,omitempty" jsonschema:"Optional tenant ID of the agent owner."`
	Config         *a2acore.SendMessageConfig `json:"configuration,omitempty" jsonschema:"Optional send configuration."`
}

type streamEventsOutput struct {
	Events []a2acore.StreamResponse `json:"events"`
}

type deleteOutput struct {
	Deleted bool `json:"deleted"`
}

// Tools returns all A2A client protocol methods as tools when at least one agent is configured.
func (c *Clients) Tools() []*tool.Tool {
	if c == nil || len(c.agents) == 0 {
		return nil
	}
	return []*tool.Tool{
		newTool(c, ToolPrefix+"send_message",
			"Send a message to an A2A agent (non-streaming). Creates a new task, continues an existing task when taskId is set, or returns a direct message response.",
			func(ctx context.Context, client *a2aclient.Client, in *sendMessageInput) (*a2acore.StreamResponse, error) {
				req, err := in.toRequest()
				if err != nil {
					return nil, err
				}
				res, err := client.SendMessage(ctx, req)
				if err != nil {
					return nil, err
				}
				return &a2acore.StreamResponse{Event: res}, nil
			}),
		newTool(c, ToolPrefix+"send_streaming_message",
			"Send a message to an A2A agent using streaming and collect all streamed events until the stream completes. Automatically falls back to non-streaming if the agent does not support streaming.",
			func(ctx context.Context, client *a2aclient.Client, in *sendMessageInput) (*streamEventsOutput, error) {
				req, err := in.toRequest()
				if err != nil {
					return nil, err
				}
				return collectStream(client.SendStreamingMessage(ctx, req))
			}),
		newTool(c, ToolPrefix+"get_task",
			"Get the current state, status, artifacts, and optional history of a task from an A2A agent.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.GetTaskRequest
			}) (*a2acore.Task, error) {
				return client.GetTask(ctx, &in.GetTaskRequest)
			}),
		newTool(c, ToolPrefix+"list_tasks",
			"List tasks from an A2A agent with optional filtering (status: TASK_STATE_SUBMITTED, TASK_STATE_WORKING, TASK_STATE_COMPLETED, TASK_STATE_FAILED, TASK_STATE_CANCELED, TASK_STATE_INPUT_REQUIRED, TASK_STATE_REJECTED, TASK_STATE_AUTH_REQUIRED; statusTimestampAfter: RFC3339) and pagination.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				listTasksReq
			}) (*a2acore.ListTasksResponse, error) {
				return client.ListTasks(ctx, (*a2acore.ListTasksRequest)(&in.listTasksReq))
			}),
		newTool(c, ToolPrefix+"cancel_task",
			"Request cancellation of an ongoing task on an A2A agent.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.CancelTaskRequest
			}) (*a2acore.Task, error) {
				return client.CancelTask(ctx, &in.CancelTaskRequest)
			}),
		newTool(c, ToolPrefix+"subscribe_to_task",
			"Subscribe to streaming updates for an existing task on an A2A agent and collect events until the stream finishes.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.SubscribeToTaskRequest
			}) (*streamEventsOutput, error) {
				return collectStream(client.SubscribeToTask(ctx, &in.SubscribeToTaskRequest))
			}),
		newTool(c, ToolPrefix+"get_task_push_config",
			"Get a specific push notification configuration for a task on an A2A agent.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.GetTaskPushConfigRequest
			}) (*a2acore.PushConfig, error) {
				return client.GetTaskPushConfig(ctx, &in.GetTaskPushConfigRequest)
			}),
		newTool(c, ToolPrefix+"list_task_push_configs",
			"List push notification configurations for a task on an A2A agent.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.ListTaskPushConfigRequest
			}) (*a2acore.ListTaskPushConfigResponse, error) {
				configs, err := client.ListTaskPushConfigs(ctx, &in.ListTaskPushConfigRequest)
				if err != nil {
					return nil, err
				}
				return &a2acore.ListTaskPushConfigResponse{Configs: configs}, nil
			}),
		newTool(c, ToolPrefix+"create_task_push_config",
			"Create a push notification configuration for a task on an A2A agent.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.PushConfig
			}) (*a2acore.PushConfig, error) {
				return client.CreateTaskPushConfig(ctx, &in.PushConfig)
			}),
		newTool(c, ToolPrefix+"delete_task_push_config",
			"Delete a push notification configuration for a task on an A2A agent.",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.DeleteTaskPushConfigRequest
			}) (*deleteOutput, error) {
				if err := client.DeleteTaskPushConfig(ctx, &in.DeleteTaskPushConfigRequest); err != nil {
					return nil, err
				}
				return &deleteOutput{Deleted: true}, nil
			}),
		newTool(c, ToolPrefix+"get_extended_agent_card",
			"Fetch the authenticated extended agent card from an A2A agent (when supported by the agent).",
			func(ctx context.Context, client *a2aclient.Client, in *struct {
				agentRef
				a2acore.GetExtendedAgentCardRequest
			}) (*a2acore.AgentCard, error) {
				return client.GetExtendedAgentCard(ctx, &in.GetExtendedAgentCardRequest)
			}),
	}
}

func newTool[In interface{ agentURL() string }, Out any](
	c *Clients,
	name, desc string,
	fn func(context.Context, *a2aclient.Client, *In) (*Out, error),
) *tool.Tool {
	return util.NewJSONTool(name, desc, func(ctx context.Context, in *In) (*Out, error) {
		client, err := c.resolveClient((*in).agentURL())
		if err != nil {
			return nil, err
		}
		return fn(ctx, client, in)
	})
}

func collectStream(seq iter.Seq2[a2acore.Event, error]) (*streamEventsOutput, error) {
	events := []a2acore.StreamResponse{}
	for ev, err := range seq {
		if err != nil {
			return nil, err
		}
		events = append(events, a2acore.StreamResponse{Event: ev})
	}
	return &streamEventsOutput{Events: events}, nil
}

func (in *sendMessageInput) toRequest() (*a2acore.SendMessageRequest, error) {
	role := in.Role
	switch strings.ToUpper(strings.TrimSpace(string(role))) {
	case "", "USER", string(a2acore.MessageRoleUser):
		role = a2acore.MessageRoleUser
	case "AGENT", string(a2acore.MessageRoleAgent):
		role = a2acore.MessageRoleAgent
	default:
		return nil, fmt.Errorf("invalid message role %q: expected ROLE_USER or ROLE_AGENT", role)
	}

	parts := make(a2acore.ContentParts, 0, len(in.Parts)+1)
	if in.Text != "" {
		parts = append(parts, a2acore.NewTextPart(in.Text))
	}
	for i, p := range in.Parts {
		part, err := p.toPart()
		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return nil, errors.New("either text or parts must be provided")
	}

	msgID := in.MessageID
	if msgID == "" {
		msgID = a2acore.NewMessageID()
	}

	return &a2acore.SendMessageRequest{
		Tenant:   in.Tenant,
		Metadata: in.Metadata,
		Config:   in.Config,
		Message: &a2acore.Message{
			ID:             msgID,
			Role:           role,
			Parts:          parts,
			TaskID:         in.TaskID,
			ContextID:      in.ContextID,
			ReferenceTasks: in.ReferenceTasks,
			Extensions:     in.Extensions,
			Metadata:       in.MessageMeta,
		},
	}, nil
}

func (p partInput) toPart() (*a2acore.Part, error) {
	var contents []a2acore.PartContent
	if p.Text != "" {
		contents = append(contents, a2acore.Text(p.Text))
	}
	if p.URL != "" {
		contents = append(contents, a2acore.URL(p.URL))
	}
	if p.Raw != "" {
		raw, err := base64.StdEncoding.DecodeString(p.Raw)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(p.Raw)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid base64 in raw: %w", err)
		}
		contents = append(contents, a2acore.Raw(raw))
	}
	if p.Data != nil {
		contents = append(contents, a2acore.Data{Value: p.Data})
	}
	if len(contents) != 1 {
		return nil, fmt.Errorf("part must specify exactly one of text, url, raw, or data (got %d)", len(contents))
	}
	return &a2acore.Part{
		Content:   contents[0],
		Filename:  p.Filename,
		MediaType: p.MediaType,
		Metadata:  p.Metadata,
	}, nil
}
