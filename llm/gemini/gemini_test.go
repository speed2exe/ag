package gemini

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
	"google.golang.org/genai"
	"google.golang.org/genai/interactions/models/interactions"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(t *testing.T, model string, apiVersion string, fn roundTripFunc) *Client {
	t.Helper()
	gc, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey: "test-api-key",
		HTTPClient: &http.Client{
			Transport: fn,
		},
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    "https://gemini.test",
			APIVersion: apiVersion,
		},
	})
	if err != nil {
		t.Fatalf("genai.NewClient() error = %v", err)
	}
	return &Client{
		client:           gc,
		model:            model,
		thoughtsByCallID: make(map[string][]interactions.ThoughtStep),
	}
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func jsonResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func collectEvents(ch <-chan models.Event) []models.Event {
	var out []models.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestNewRequiresModel(t *testing.T) {
	_, err := New(nil, &setting.Settings{GeminiApiKey: "test-key"})
	if err == nil {
		t.Fatal("New() with empty GeminiModel expected error, got nil")
	}
}

func TestNewConfiguresClient(t *testing.T) {
	c, err := New(nil, &setting.Settings{
		GeminiApiKey:  "my-key",
		GeminiBaseURL: "https://example.invalid",
		GeminiModel:   "gemini-4-hydrogen",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.model != "gemini-4-hydrogen" {
		t.Fatalf("model = %q, want %q", c.model, "gemini-4-hydrogen")
	}
	cfg := c.client.ClientConfig()
	if cfg.APIKey != "my-key" {
		t.Fatalf("APIKey = %q, want %q", cfg.APIKey, "my-key")
	}
	if cfg.HTTPOptions.BaseURL != "https://example.invalid" {
		t.Fatalf("BaseURL = %q, want %q", cfg.HTTPOptions.BaseURL, "https://example.invalid")
	}
	if cfg.HTTPOptions.APIVersion != "v1beta" {
		t.Fatalf("APIVersion = %q, want %q", cfg.HTTPOptions.APIVersion, "v1beta")
	}
	if _, found, err := c.CallTool(context.Background(), "unknown", "{}"); found || err != nil {
		t.Fatalf("CallTool() = (_, %v, %v), want (_, false, nil)", found, err)
	}
}

func TestToInteractionsStepsDropsDuplicateCallIds(t *testing.T) {
	c := &Client{}
	duplicated := &models.ToolCalls{Items: []*models.ToolCall{
		{CallId: "call_1", Name: "bash", Args: `{"command":"echo one"}`, Result: "one"},
	}}
	messages := []models.Message{
		&models.UserPrompt{Text: "run it"},
		duplicated,
		duplicated,
		&models.ToolCalls{Items: []*models.ToolCall{
			{CallId: "call_1", Name: "bash", Args: `{"command":"echo one"}`, Result: "one"},
			{CallId: "call_2", Name: "bash", Args: `{"command":"echo two"}`, Result: "two"},
		}},
	}

	steps := c.toInteractionsSteps(messages)
	var calls, results []string
	for _, s := range steps {
		if s.FunctionCallStep != nil {
			calls = append(calls, s.FunctionCallStep.ID)
		}
		if s.FunctionResultStep != nil {
			results = append(results, s.FunctionResultStep.CallID)
		}
	}
	if want := []string{"call_1", "call_2"}; !slices.Equal(calls, want) {
		t.Fatalf("function_call ids = %v, want %v", calls, want)
	}
	if want := []string{"call_1", "call_2"}; !slices.Equal(results, want) {
		t.Fatalf("function_result call_ids = %v, want %v", results, want)
	}
}

func TestToInteractionsStepsMapping(t *testing.T) {
	c := &Client{}
	c.cacheThoughts("c1", []interactions.ThoughtStep{
		{
			Signature: new("sig_1"),
			Summary: []interactions.ThoughtSummaryContent{
				interactions.NewThoughtSummaryContent(interactions.TextContent{Text: "cached reasoning"}),
			},
		},
	})

	messages := []models.Message{
		&models.Summary{Text: "prior summary"},
		&models.UserPrompt{Text: "hello"},
		&models.AgentReasoning{Reasoning: "reasoning from context"},
		&models.ToolCalls{Items: []*models.ToolCall{
			{CallId: "c1", Name: "tool_a", Args: "", Result: "res_a"},
			{CallId: "c2", Name: "tool_b", Args: "not-json", Result: "res_b"},
		}},
		&models.AgentResponse{Text: "final answer"},
		&models.OutputErr{Err: "some error"},
	}

	steps := c.toInteractionsSteps(messages)
	// Expected steps:
	// 0: user_input (summary)
	// 1: user_input (prompt)
	// (AgentReasoning skipped)
	// 2: thought (cached for c1)
	// 3: function_call (c1)
	// 4: function_call (c2)
	// 5: function_result (c1)
	// 6: function_result (c2)
	// 7: model_output (final answer)
	// 8: model_output (some error)
	if len(steps) != 9 {
		t.Fatalf("len(steps) = %d, want 9", len(steps))
	}

	if steps[0].UserInputStep == nil || steps[0].UserInputStep.Content[0].TextContent.Text != "prior summary" {
		t.Fatalf("step[0] = %#v, want summary user_input", steps[0])
	}
	if steps[1].UserInputStep == nil || steps[1].UserInputStep.Content[0].TextContent.Text != "hello" {
		t.Fatalf("step[1] = %#v, want prompt user_input", steps[1])
	}
	if steps[2].ThoughtStep == nil || steps[2].ThoughtStep.Signature == nil || *steps[2].ThoughtStep.Signature != "sig_1" {
		t.Fatalf("step[2] = %#v, want thought with sig_1", steps[2])
	}
	if len(steps[2].ThoughtStep.Summary) != 1 || steps[2].ThoughtStep.Summary[0].TextContent.Text != "cached reasoning" {
		t.Fatalf("step[2].Summary = %#v, want cached reasoning", steps[2].ThoughtStep.Summary)
	}
	if steps[3].FunctionCallStep == nil || steps[3].FunctionCallStep.ID != "c1" || len(steps[3].FunctionCallStep.Arguments) != 0 {
		t.Fatalf("step[3] = %#v, want function_call c1 with empty map", steps[3])
	}
	if steps[4].FunctionCallStep == nil || steps[4].FunctionCallStep.ID != "c2" || steps[4].FunctionCallStep.Arguments["raw"] != "not-json" {
		t.Fatalf("step[4] = %#v, want function_call c2 with raw fallback", steps[4])
	}
	if steps[5].FunctionResultStep == nil || steps[5].FunctionResultStep.CallID != "c1" || steps[5].FunctionResultStep.Result.ArrayOfFunctionResultSubcontent[0].TextContent.Text != "res_a" {
		t.Fatalf("step[5] = %#v, want function_result c1", steps[5])
	}
	if steps[6].FunctionResultStep == nil || steps[6].FunctionResultStep.CallID != "c2" || steps[6].FunctionResultStep.Result.ArrayOfFunctionResultSubcontent[0].TextContent.Text != "res_b" {
		t.Fatalf("step[6] = %#v, want function_result c2", steps[6])
	}
	if steps[7].ModelOutputStep == nil || steps[7].ModelOutputStep.Content[0].TextContent.Text != "final answer" {
		t.Fatalf("step[7] = %#v, want model_output final answer", steps[7])
	}
	if steps[8].ModelOutputStep == nil || steps[8].ModelOutputStep.Content[0].TextContent.Text != "some error" {
		t.Fatalf("step[8] = %#v, want model_output some error", steps[8])
	}
}

func TestToInteractionsTools(t *testing.T) {
	if got := toInteractionsTools(nil); got != nil {
		t.Fatalf("toInteractionsTools(nil) = %#v, want nil", got)
	}
	desc := "Run a command"
	tools := []*tool.Tool{
		{
			Name:        "bash_command",
			Description: &desc,
			InputSchema: []byte(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		},
	}
	got := toInteractionsTools(tools)
	if len(got) != 1 || got[0].Function == nil {
		t.Fatalf("got = %#v, want 1 function tool", got)
	}
	fn := got[0].Function
	if fn.Name == nil || *fn.Name != "bash_command" {
		t.Fatalf("fn.Name = %v, want bash_command", fn.Name)
	}
	if fn.Description == nil || *fn.Description != desc {
		t.Fatalf("fn.Description = %v, want %q", fn.Description, desc)
	}
	params, ok := fn.Parameters.(map[string]any)
	if !ok || params["type"] != "object" {
		t.Fatalf("fn.Parameters = %#v, want object schema", fn.Parameters)
	}
}

func TestGenerateStreamThoughtTextAndToolCalls(t *testing.T) {
	var capturedPath string
	var capturedAPIKey string
	var capturedRevision string
	var capturedBody map[string]any

	sse := strings.Join([]string{
		`data: {"event_type":"interaction.created","interaction":{"id":"int_1","status":"in_progress"}}`,
		``,
		`data: {"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
		``,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"Thinking..."}}}`,
		``,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_abc"}}`,
		``,
		`data: {"event_type":"step.stop","index":0}`,
		``,
		`data: {"event_type":"step.start","index":1,"step":{"type":"model_output","content":[{"type":"text","text":"Hello"}]}}`,
		``,
		`data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":" world"}}`,
		``,
		`data: {"event_type":"step.stop","index":1}`,
		``,
		`data: {"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_1","name":"bash_command","arguments":{}}}`,
		``,
		`data: {"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"command\":"}}`,
		``,
		`data: {"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"\"ls\"}"}}`,
		``,
		`data: {"event_type":"step.stop","index":2}`,
		``,
		`data: {"event_type":"step.start","index":3,"step":{"type":"function_call","id":"call_2","name":"no_arg_tool","arguments":{}}}`,
		``,
		`data: {"event_type":"step.stop","index":3}`,
		``,
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_1","status":"requires_action","usage":{"total_input_tokens":100,"total_cached_tokens":40,"total_output_tokens":25,"total_thought_tokens":10}}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	client := newTestClient(t, "gemini-4-hydrogen", "v1beta", func(req *http.Request) (*http.Response, error) {
		capturedPath = req.URL.Path
		capturedAPIKey = req.Header.Get("x-goog-api-key")
		capturedRevision = req.Header.Get("Api-Revision")
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(raw, &capturedBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		return sseResponse(sse), nil
	})

	desc := "Run bash"
	events := collectEvents(client.GenerateStream(
		context.Background(),
		[]string{"Be concise.", "Use tools carefully."},
		[]*tool.Tool{{
			Name:        "bash_command",
			Description: &desc,
			InputSchema: []byte(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		}},
		[]models.Message{
			&models.UserPrompt{Text: "List files"},
		},
	))

	// Verify HTTP request details
	if capturedPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", capturedPath)
	}
	if capturedAPIKey != "test-api-key" {
		t.Fatalf("x-goog-api-key = %q, want test-api-key", capturedAPIKey)
	}
	if capturedRevision == "" {
		t.Fatal("Api-Revision header was not sent")
	}
	if capturedBody["model"] != "gemini-4-hydrogen" {
		t.Fatalf("body.model = %v, want gemini-4-hydrogen", capturedBody["model"])
	}
	if capturedBody["store"] != false {
		t.Fatalf("body.store = %v, want false", capturedBody["store"])
	}
	if capturedBody["stream"] != true {
		t.Fatalf("body.stream = %v, want true", capturedBody["stream"])
	}
	if capturedBody["system_instruction"] != "Be concise.\n\nUse tools carefully." {
		t.Fatalf("body.system_instruction = %v", capturedBody["system_instruction"])
	}
	genCfg, _ := capturedBody["generation_config"].(map[string]any)
	if genCfg["thinking_summaries"] != "auto" {
		t.Fatalf("body.generation_config = %v, want thinking_summaries=auto", genCfg)
	}

	// Verify emitted events (thought_signature is cached internally, not emitted)
	want := []models.Event{
		&models.OutputReasoning{Reasoning: "Thinking..."},
		&models.OutputText{Text: "Hello"},
		&models.OutputText{Text: " world"},
		&models.OutputFnCall{CallId: "call_1", Name: "bash_command"},
		&models.OutputFnArgs{Args: `{"command":`},
		&models.OutputFnArgs{Args: `"ls"}`},
		&models.OutputFnCall{CallId: "call_2", Name: "no_arg_tool"},
		&models.OutputFnArgs{Args: "{}"},
		&models.TokenUsage{
			InputTokens:           100,
			InputTokensCached:     40,
			OutputTokens:          25,
			OutputTokensReasoning: 10,
		},
	}

	if len(events) != len(want) {
		t.Fatalf("got %d events (%#v), want %d (%#v)", len(events), events, len(want), want)
	}
	for i := range want {
		gotJSON, _ := json.Marshal(events[i])
		wantJSON, _ := json.Marshal(want[i])
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("event[%d] = %T %s, want %T %s", i, events[i], gotJSON, want[i], wantJSON)
		}
	}

	// Verify thought signature was cached against the first function call only
	cached := client.lookupThoughts("call_1")
	if len(cached) != 1 || cached[0].Signature == nil || *cached[0].Signature != "sig_abc" {
		t.Fatalf("lookupThoughts(call_1) = %#v, want sig_abc", cached)
	}
	if cached2 := client.lookupThoughts("call_2"); len(cached2) != 0 {
		t.Fatalf("lookupThoughts(call_2) = %#v, want empty", cached2)
	}
}

func TestGenerateStreamErrors(t *testing.T) {
	t.Run("sse error event", func(t *testing.T) {
		sse := strings.Join([]string{
			`data: {"event_type":"error","error":{"code":"invalid_request","message":"bad prompt"}}`,
			``,
			`data: {"event_type":"interaction.completed","interaction":{"id":"int_err","status":"failed"}}`,
			``,
			`data: [DONE]`,
			``,
			``,
		}, "\n")
		client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
			return sseResponse(sse), nil
		})
		events := collectEvents(client.GenerateStream(context.Background(), nil, nil, []models.Message{
			&models.UserPrompt{Text: "hi"},
		}))
		if len(events) != 1 {
			t.Fatalf("got %d events (%#v), want 1 OutputErr", len(events), events)
		}
		errEv, ok := events[0].(*models.OutputErr)
		if !ok || !strings.Contains(errEv.Err, "bad prompt") || !strings.Contains(errEv.Err, "invalid_request") {
			t.Fatalf("events[0] = %#v, want OutputErr containing invalid_request and bad prompt", events[0])
		}
	})

	t.Run("truncated sse stream", func(t *testing.T) {
		sse := strings.Join([]string{
			`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"partial"}}`,
			``,
			``,
		}, "\n")
		client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
			return sseResponse(sse), nil
		})
		events := collectEvents(client.GenerateStream(context.Background(), nil, nil, []models.Message{
			&models.UserPrompt{Text: "hi"},
		}))
		if len(events) != 2 {
			t.Fatalf("got %d events (%#v), want text + OutputErr", len(events), events)
		}
		if _, ok := events[1].(*models.OutputErr); !ok {
			t.Fatalf("events[1] = %T, want *models.OutputErr", events[1])
		}
	})

	t.Run("http 400 error", func(t *testing.T) {
		client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusBadRequest, `{"error":{"code":"invalid_argument","message":"missing signature"}}`), nil
		})
		events := collectEvents(client.GenerateStream(context.Background(), nil, nil, []models.Message{
			&models.UserPrompt{Text: "hi"},
		}))
		if len(events) != 1 {
			t.Fatalf("got %d events (%#v), want 1 OutputErr", len(events), events)
		}
		if _, ok := events[0].(*models.OutputErr); !ok {
			t.Fatalf("events[0] = %T, want *models.OutputErr", events[0])
		}
	})
}

func TestGenerateStreamUnaryFallback(t *testing.T) {
	body := `{
		"id": "int_unary",
		"status": "completed",
		"steps": [
			{"type": "thought", "signature": "sig_u", "summary": [{"type": "text", "text": "plan"}]},
			{"type": "model_output", "content": [{"type": "text", "text": "done"}]},
			{"type": "function_call", "id": "fc_1", "name": "ping", "arguments": {"host": "localhost"}}
		],
		"usage": {"total_input_tokens": 12, "total_cached_tokens": 3, "total_output_tokens": 8, "total_thought_tokens": 2}
	}`
	client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})
	events := collectEvents(client.GenerateStream(context.Background(), nil, nil, []models.Message{
		&models.UserPrompt{Text: "hi"},
	}))
	if len(events) != 5 {
		t.Fatalf("got %d events (%#v), want 5", len(events), events)
	}
	if r, ok := events[0].(*models.OutputReasoning); !ok || r.Reasoning != "plan" {
		t.Fatalf("events[0] = %#v, want reasoning plan", events[0])
	}
	if txt, ok := events[1].(*models.OutputText); !ok || txt.Text != "done" {
		t.Fatalf("events[1] = %#v, want text done", events[1])
	}
	if fc, ok := events[2].(*models.OutputFnCall); !ok || fc.CallId != "fc_1" || fc.Name != "ping" {
		t.Fatalf("events[2] = %#v, want fn call fc_1/ping", events[2])
	}
	if args, ok := events[3].(*models.OutputFnArgs); !ok || args.Args != `{"host":"localhost"}` {
		t.Fatalf("events[3] = %#v, want fn args", events[3])
	}
	if usage, ok := events[4].(*models.TokenUsage); !ok || usage.InputTokens != 12 || usage.InputTokensCached != 3 || usage.OutputTokens != 8 || usage.OutputTokensReasoning != 2 {
		t.Fatalf("events[4] = %#v, want usage", events[4])
	}
	if cached := client.lookupThoughts("fc_1"); len(cached) != 1 || cached[0].Signature == nil || *cached[0].Signature != "sig_u" {
		t.Fatalf("lookupThoughts(fc_1) = %#v, want sig_u", cached)
	}
}

func TestStepStartFunctionCallWithInlineArguments(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"event_type":"step.start","index":0,"step":{"type":"thought","signature":"sig_inline","summary":[{"type":"text","text":"quick thought"}]}}`,
		``,
		`data: {"event_type":"step.start","index":1,"step":{"type":"function_call","id":"call_inline","name":"echo","arguments":{"msg":"hi"}}}`,
		``,
		`data: {"event_type":"step.stop","index":1}`,
		``,
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_2","status":"requires_action"}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
		return sseResponse(sse), nil
	})
	events := collectEvents(client.GenerateStream(context.Background(), nil, nil, []models.Message{
		&models.UserPrompt{Text: "hi"},
	}))
	if len(events) != 3 {
		t.Fatalf("got %d events (%#v), want 3", len(events), events)
	}
	if r, ok := events[0].(*models.OutputReasoning); !ok || r.Reasoning != "quick thought" {
		t.Fatalf("events[0] = %#v, want inline thought", events[0])
	}
	if fc, ok := events[1].(*models.OutputFnCall); !ok || fc.CallId != "call_inline" || fc.Name != "echo" {
		t.Fatalf("events[1] = %#v, want fn call", events[1])
	}
	if args, ok := events[2].(*models.OutputFnArgs); !ok || args.Args != `{"msg":"hi"}` {
		t.Fatalf("events[2] = %#v, want inline args", events[2])
	}
	if cached := client.lookupThoughts("call_inline"); len(cached) != 1 || cached[0].Signature == nil || *cached[0].Signature != "sig_inline" {
		t.Fatalf("lookupThoughts(call_inline) = %#v, want sig_inline", cached)
	}
}

func TestStepStartModelOutputError(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"event_type":"step.start","index":0,"step":{"type":"model_output","error":{"code":3,"message":"blocked by safety"}}}`,
		``,
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_3","status":"failed"}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
		return sseResponse(sse), nil
	})
	events := collectEvents(client.GenerateStream(context.Background(), nil, nil, []models.Message{
		&models.UserPrompt{Text: "hi"},
	}))
	if len(events) != 1 {
		t.Fatalf("got %d events (%#v), want 1 OutputErr", len(events), events)
	}
	errEv, ok := events[0].(*models.OutputErr)
	if !ok || !strings.Contains(errEv.Err, "blocked by safety") {
		t.Fatalf("events[0] = %#v, want OutputErr containing blocked by safety", events[0])
	}
}

func TestGenerateStreamMultiTurnThoughtSignatureReplay(t *testing.T) {
	turn := 0
	var turn2Steps []map[string]any

	client := newTestClient(t, "gemini-4-hydrogen", "v1", func(req *http.Request) (*http.Response, error) {
		turn++
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var body struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if turn == 1 {
			sse := strings.Join([]string{
				`data: {"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
				``,
				`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"Check pwd"}}}`,
				``,
				`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_turn_1"}}`,
				``,
				`data: {"event_type":"step.stop","index":0}`,
				``,
				`data: {"event_type":"step.start","index":1,"step":{"type":"function_call","id":"call_pwd","name":"bash_command","arguments":{}}}`,
				``,
				`data: {"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"command\":\"pwd\"}"}}`,
				``,
				`data: {"event_type":"step.stop","index":1}`,
				``,
				`data: {"event_type":"interaction.completed","interaction":{"id":"int_t1","status":"requires_action"}}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n")
			return sseResponse(sse), nil
		}

		turn2Steps = body.Input
		sse := strings.Join([]string{
			`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
			``,
			`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"You are in /tmp."}}`,
			``,
			`data: {"event_type":"step.stop","index":0}`,
			``,
			`data: {"event_type":"interaction.completed","interaction":{"id":"int_t2","status":"completed"}}`,
			``,
			`data: [DONE]`,
			``,
			``,
		}, "\n")
		return sseResponse(sse), nil
	})

	// Turn 1
	messages := []models.Message{&models.UserPrompt{Text: "Where am I?"}}
	t1Events := collectEvents(client.GenerateStream(context.Background(), nil, nil, messages))

	// Emulate core.go collecting reasoning (without any signature field) and tool call
	var reasoning, callID, callName, callArgs string
	for _, ev := range t1Events {
		switch e := ev.(type) {
		case *models.OutputReasoning:
			reasoning += e.Reasoning
		case *models.OutputFnCall:
			callID, callName = e.CallId, e.Name
		case *models.OutputFnArgs:
			callArgs += e.Args
		}
	}
	messages = append(messages,
		&models.AgentReasoning{Reasoning: reasoning},
		&models.ToolCalls{Items: []*models.ToolCall{{
			CallId: callID,
			Name:   callName,
			Args:   callArgs,
			Result: "/tmp",
		}}},
	)

	// Turn 2
	t2Events := collectEvents(client.GenerateStream(context.Background(), nil, nil, messages))
	if len(t2Events) != 1 {
		t.Fatalf("turn 2 events = %#v, want 1 text event", t2Events)
	}
	if txt, ok := t2Events[0].(*models.OutputText); !ok || txt.Text != "You are in /tmp." {
		t.Fatalf("turn 2 event = %#v, want OutputText", t2Events[0])
	}

	if len(turn2Steps) != 4 {
		t.Fatalf("turn 2 input steps len = %d (%#v), want 4", len(turn2Steps), turn2Steps)
	}
	if turn2Steps[0]["type"] != "user_input" {
		t.Fatalf("step[0].type = %v, want user_input", turn2Steps[0]["type"])
	}
	if turn2Steps[1]["type"] != "thought" || turn2Steps[1]["signature"] != "sig_turn_1" {
		t.Fatalf("step[1] = %#v, want thought with signature sig_turn_1", turn2Steps[1])
	}
	if turn2Steps[2]["type"] != "function_call" || turn2Steps[2]["id"] != "call_pwd" || turn2Steps[2]["name"] != "bash_command" {
		t.Fatalf("step[2] = %#v, want function_call call_pwd", turn2Steps[2])
	}
	if turn2Steps[3]["type"] != "function_result" || turn2Steps[3]["call_id"] != "call_pwd" {
		t.Fatalf("step[3] = %#v, want function_result call_pwd", turn2Steps[3])
	}
}
