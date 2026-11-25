package builtin_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/state"
	"github.com/speed2exe/ag/store"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/builtin"
	"github.com/speed2exe/ag/tool/skill"
)

type recordedCall struct {
	instructions []string
	tools        []*tool.Tool
	messages     []models.Message
}

type mockModel struct {
	mu      sync.Mutex
	calls   []recordedCall
	handler func(callIdx int, instructions []string, tools []*tool.Tool, messages []models.Message) []models.Event
}

func (m *mockModel) GenerateStream(
	ctx context.Context,
	instructions []string,
	tools []*tool.Tool,
	messages []models.Message,
) <-chan models.Event {
	m.mu.Lock()
	idx := len(m.calls)
	copiedMsgs := make([]models.Message, len(messages))
	copy(copiedMsgs, messages)
	m.calls = append(m.calls, recordedCall{
		instructions: instructions,
		tools:        tools,
		messages:     copiedMsgs,
	})
	m.mu.Unlock()

	evs := m.handler(idx, instructions, tools, copiedMsgs)
	ch := make(chan models.Event, len(evs))
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	return ch
}

func newTestContext(t *testing.T, model *mockModel, toolsByName map[string]*tool.Tool) (context.Context, *store.CtxStore) {
	t.Helper()
	ctxStore := store.NewCtxStore(t.TempDir())
	var out bytes.Buffer
	ctx := state.WithState(context.Background(), &state.State{
		Printer: printer.New(t.Context(), &out),
		Settings: &setting.Settings{
			MaxMessagesBeforeUserPrompt: 32,
			MaxMessagesAfterUserPrompt:  64,
		},
		Llm:         model,
		ToolsByName: toolsByName,
		Skills: &skill.Skills{
			SkillByFilePath:        map[string]*skill.Skill{},
			PreloadSkillByFilePath: map[string]string{},
		},
		CtxStore: ctxStore,
	})
	return ctx, ctxStore
}

func drain(ch <-chan models.Event) []models.Event {
	var out []models.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestCompactToolSchemaAndOutsideStream(t *testing.T) {
	ct := builtin.Compact()
	if ct.Name != consts.CompactToolName {
		t.Fatalf("expected tool name %q, got %q", consts.CompactToolName, ct.Name)
	}
	if ct.Description == nil || *ct.Description == "" {
		t.Fatal("expected non-empty description")
	}

	// Verify schema unmarshals cleanly for OpenAI/Gemini (map[string]any) and Anthropic.
	var generic map[string]any
	if err := json.Unmarshal(ct.InputSchema, &generic); err != nil {
		t.Fatalf("failed to unmarshal schema as generic JSON: %v", err)
	}
	var anSchema anthropic.ToolInputSchemaParam
	if err := anSchema.UnmarshalJSON(ct.InputSchema); err != nil {
		t.Fatalf("failed to unmarshal schema for Anthropic: %v", err)
	}

	// Calling compact outside an active GenerateStream should fail cleanly.
	if _, err := ct.Call(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("expected error when calling compact outside GenerateStream")
	}
}

func TestCompactToolCompactsMultiTurnAndPersistsCleanly(t *testing.T) {
	compactTool := builtin.Compact()
	echoTool := &tool.Tool{
		Name:        "echo",
		Description: new("echo"),
		InputSchema: []byte(`{"type":"object"}`),
		Call: func(ctx context.Context, args []byte) ([]byte, error) {
			return []byte(`"echoed-result"`), nil
		},
	}

	model := &mockModel{}
	model.handler = func(callIdx int, instructions []string, tools []*tool.Tool, messages []models.Message) []models.Event {
		switch callIdx {
		case 0:
			// Turn calls echo and compact in parallel while well below message limits.
			return []models.Event{
				&models.OutputFnCall{CallId: "c1", Name: "echo"},
				&models.OutputFnArgs{Args: `{}`},
				&models.OutputFnCall{CallId: "c2", Name: consts.CompactToolName},
				&models.OutputFnArgs{Args: `{"instructions":"keep foo.go findings"}`},
			}
		case 1:
			// Summarization call (no tools, no instructions).
			if len(tools) != 0 {
				t.Errorf("expected no tools during compaction summarization, got %d", len(tools))
			}
			return []models.Event{
				&models.OutputText{Text: "# Compacted conversation context\nSummary with foo.go findings."},
			}
		case 2:
			// Post-compaction continuation turn.
			return []models.Event{
				&models.OutputText{Text: "Continuing after compaction."},
			}
		default:
			t.Fatalf("unexpected model call %d", callIdx)
			return nil
		}
	}

	ctx, ctxStore := newTestContext(t, model, map[string]*tool.Tool{
		consts.CompactToolName: compactTool,
		"echo":                 echoTool,
	})

	// Seed prior multi-turn context in store (well below MaxMessagesBeforeUserPrompt=32).
	prevCtxId := "prev-ctx"
	if err := ctxStore.Save(prevCtxId, []models.Message{
		&models.UserPrompt{Text: "earlier question"},
		&models.AgentResponse{Text: "earlier answer"},
	}); err != nil {
		t.Fatalf("failed to seed prior context: %v", err)
	}

	newCtxId, events := core.GenerateStream(ctx, model, []*tool.Tool{compactTool, echoTool}, []string{"sys"}, prevCtxId, "current task")
	drain(events)

	if len(model.calls) != 3 {
		t.Fatalf("expected 3 model calls (tool turn, summarize, continue), got %d", len(model.calls))
	}

	// Verify summarization call received earlier messages, current prompt, both tool results, and custom instructions.
	sumCall := model.calls[1]
	lastPrompt, ok := sumCall.messages[len(sumCall.messages)-1].(*models.UserPrompt)
	if !ok {
		t.Fatalf("expected last message of summarization request to be UserPrompt, got %T", sumCall.messages[len(sumCall.messages)-1])
	}
	if !strings.Contains(lastPrompt.Text, consts.SummarizerCompact) {
		t.Fatal("expected summarization prompt to contain SummarizerCompact")
	}
	if !strings.Contains(lastPrompt.Text, "keep foo.go findings") {
		t.Fatal("expected summarization prompt to contain caller instructions")
	}
	var sawEchoResult bool
	for _, msg := range sumCall.messages {
		if tcs, ok := msg.(*models.ToolCalls); ok {
			for _, tc := range tcs.Items {
				if tc.Name == "echo" && tc.Result == `"echoed-result"` {
					sawEchoResult = true
				}
			}
		}
	}
	if !sawEchoResult {
		t.Fatal("expected parallel echo tool result to be included before compaction")
	}

	// Verify post-compaction call received ONLY [lastUserPrompt, Summary].
	contCall := model.calls[2]
	if len(contCall.messages) != 2 {
		t.Fatalf("expected 2 compacted messages for continuation call, got %d: %#v", len(contCall.messages), contCall.messages)
	}
	if up, ok := contCall.messages[0].(*models.UserPrompt); !ok || up.Text != "current task" {
		t.Fatalf("expected first compacted message to be active UserPrompt, got %#v", contCall.messages[0])
	}
	if sum, ok := contCall.messages[1].(*models.Summary); !ok || !strings.Contains(sum.Text, "foo.go findings") {
		t.Fatalf("expected second compacted message to be Summary, got %#v", contCall.messages[1])
	}

	// Verify persisted context in CtxStore has [UserPrompt, Summary, AgentResponse] and NO duplicated summary response or old turns.
	saved, err := ctxStore.Load(newCtxId)
	if err != nil {
		t.Fatalf("failed to load saved context: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("expected 3 saved messages [UserPrompt, Summary, AgentResponse], got %d: %#v", len(saved), saved)
	}
	if up, ok := saved[0].(*models.UserPrompt); !ok || up.Text != "current task" {
		t.Fatalf("unexpected saved[0]: %#v", saved[0])
	}
	if sum, ok := saved[1].(*models.Summary); !ok || !strings.Contains(sum.Text, "foo.go findings") {
		t.Fatalf("unexpected saved[1]: %#v", saved[1])
	}
	if resp, ok := saved[2].(*models.AgentResponse); !ok || resp.Text != "Continuing after compaction." {
		t.Fatalf("unexpected saved[2]: %#v", saved[2])
	}
}

func TestCompactSubagentIsolation(t *testing.T) {
	compactTool := builtin.Compact()
	agentTool := builtin.Agent()

	model := &mockModel{}
	model.handler = func(callIdx int, instructions []string, tools []*tool.Tool, messages []models.Message) []models.Event {
		switch callIdx {
		case 0:
			// Parent spawns subagent with compact tool.
			return []models.Event{
				&models.OutputFnCall{CallId: "parent-1", Name: consts.AgentToolName},
				&models.OutputFnArgs{Args: `{"tool_names":["compact"],"prompt":"subtask"}`},
			}
		case 1:
			// Subagent calls compact.
			return []models.Event{
				&models.OutputFnCall{CallId: "sub-1", Name: consts.CompactToolName},
				&models.OutputFnArgs{Args: `{}`},
			}
		case 2:
			// Subagent compaction summary.
			return []models.Event{
				&models.OutputText{Text: "# Compacted conversation context\nSubagent summary."},
			}
		case 3:
			// Subagent finishes after compaction.
			return []models.Event{
				&models.OutputText{Text: "Subagent done."},
			}
		case 4:
			// Parent receives subagent result WITHOUT parent being compacted!
			return []models.Event{
				&models.OutputText{Text: "Parent done."},
			}
		default:
			t.Fatalf("unexpected model call %d", callIdx)
			return nil
		}
	}

	toolsByName := map[string]*tool.Tool{
		consts.CompactToolName: compactTool,
		consts.AgentToolName:   agentTool,
	}
	ctx, _ := newTestContext(t, model, toolsByName)

	_, events := core.GenerateStream(ctx, model, []*tool.Tool{agentTool, compactTool}, nil, "", "parent task")
	drain(events)

	if len(model.calls) != 5 {
		t.Fatalf("expected 5 model calls (parent, subagent, subagent summary, subagent continue, parent continue), got %d", len(model.calls))
	}
	// Parent's continuation call (index 4) should still contain the original UserPrompt and ToolCalls (NOT a Summary).
	parentCont := model.calls[4]
	if len(parentCont.messages) != 2 {
		t.Fatalf("expected parent continuation to have [UserPrompt, ToolCalls], got %d: %#v", len(parentCont.messages), parentCont.messages)
	}
	if _, ok := parentCont.messages[1].(*models.ToolCalls); !ok {
		t.Fatalf("expected parent second message to be *models.ToolCalls, got %T", parentCont.messages[1])
	}
}

func TestCompactErrorDoesNotLoopOrLoseMessages(t *testing.T) {
	compactTool := builtin.Compact()

	model := &mockModel{}
	model.handler = func(callIdx int, instructions []string, tools []*tool.Tool, messages []models.Message) []models.Event {
		switch callIdx {
		case 0:
			return []models.Event{
				&models.OutputFnCall{CallId: "c1", Name: consts.CompactToolName},
				&models.OutputFnArgs{Args: `{}`},
			}
		case 1:
			// Compaction summarization fails.
			return []models.Event{
				&models.OutputErr{Err: "temporary error"},
			}
		case 2:
			// Model continues with uncompacted messages and finishes without looping.
			return []models.Event{
				&models.OutputText{Text: "Recovered after compaction error."},
			}
		default:
			t.Fatalf("unexpected model call %d", callIdx)
			return nil
		}
	}

	ctx, _ := newTestContext(t, model, map[string]*tool.Tool{
		consts.CompactToolName: compactTool,
	})
	_, events := core.GenerateStream(ctx, model, []*tool.Tool{compactTool}, nil, "", "do task")
	drain(events)

	if len(model.calls) != 3 {
		t.Fatalf("expected 3 model calls, got %d", len(model.calls))
	}
	// Call 2 should still have [UserPrompt, ToolCalls] intact.
	if len(model.calls[2].messages) != 2 {
		t.Fatalf("expected 2 uncompacted messages after failed compaction, got %d", len(model.calls[2].messages))
	}
	if _, ok := model.calls[2].messages[1].(*models.ToolCalls); !ok {
		t.Fatalf("expected ToolCalls to remain intact, got %T", model.calls[2].messages[1])
	}
}

func TestCompactEventPrinterIntegration(t *testing.T) {
	compactTool := builtin.Compact()

	model := &mockModel{}
	model.handler = func(callIdx int, instructions []string, tools []*tool.Tool, messages []models.Message) []models.Event {
		switch callIdx {
		case 0:
			return []models.Event{
				&models.OutputFnCall{CallId: "c1", Name: consts.CompactToolName},
				&models.OutputFnArgs{Args: `{}`},
			}
		case 1:
			return []models.Event{
				&models.OutputText{Text: "# Compacted conversation context\nDone."},
			}
		case 2:
			return []models.Event{
				&models.OutputText{Text: "All compacted!"},
			}
		default:
			t.Fatalf("unexpected model call %d", callIdx)
			return nil
		}
	}

	ctx, _ := newTestContext(t, model, map[string]*tool.Tool{
		consts.CompactToolName: compactTool,
	})
	st := state.GetState(ctx)
	ep := llm.NewEventPrinter(st.Printer)

	_, events := core.GenerateStream(ctx, model, []*tool.Tool{compactTool}, nil, "", "compact now")
	for ev := range events {
		ep.Print(ev)
	}
	ep.FinishCurrentOutput()
	st.Printer.Flush()
}
