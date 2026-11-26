package a2aserver

import (
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"mime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/speed2exe/ag/app"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/state"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/util"
)

const (
	maxInlineRawBytes     = 1 << 20 // 1 MiB max inline UTF-8 raw part
	maxRawPreviewBase64   = 2048
	defaultArtifactName   = "response"
	defaultOutputMimeType = "text/plain"
)

// AgentExecutor adapts the ag agentic loop (core.GenerateStream) to the
// a2asrv.AgentExecutor and a2asrv.AgentExecutionCleaner interfaces.
type AgentExecutor struct {
	runtime      *app.Runtime
	toolsByName  map[string]*tool.Tool
	tools        []*tool.Tool
	instructions []string
	outputModes  []string
	contextMap   *ContextMapStore

	mu     sync.Mutex
	active map[a2a.TaskID]*activeRun
}

type activeRun struct {
	cancel   context.CancelFunc
	canceled atomic.Bool
}

var (
	_ a2asrv.AgentExecutor         = (*AgentExecutor)(nil)
	_ a2asrv.AgentExecutionCleaner = (*AgentExecutor)(nil)
)

// NewAgentExecutor constructs an AgentExecutor backed by rt and contextMap.
func NewAgentExecutor(rt *app.Runtime, contextMap *ContextMapStore, outputModes []string) *AgentExecutor {
	toolsByName := make(map[string]*tool.Tool, len(rt.ToolsByName)+1)
	maps.Copy(toolsByName, rt.ToolsByName)
	if _, exists := toolsByName[consts.A2ARequestInputToolName]; !exists {
		toolsByName[consts.A2ARequestInputToolName] = RequestInputTool()
	}

	instructions := append(slices.Clone(rt.Instructions), a2aSystemInstruction)

	parsedModes := make([]string, 0, len(outputModes))
	for _, m := range outputModes {
		if mt, _, err := mime.ParseMediaType(m); err == nil {
			parsedModes = append(parsedModes, mt)
		}
	}
	if len(parsedModes) == 0 {
		parsedModes = []string{defaultOutputMimeType}
	}

	return &AgentExecutor{
		runtime:      rt,
		toolsByName:  toolsByName,
		tools:        util.ToolSortedListFromMap(toolsByName),
		instructions: instructions,
		outputModes:  parsedModes,
		contextMap:   contextMap,
		active:       make(map[a2a.TaskID]*activeRun),
	}
}

// Tools returns the sorted tool list exposed to the LLM (including a2a_request_input).
func (e *AgentExecutor) Tools() []*tool.Tool {
	return e.tools
}

// Execute runs one agent turn for execCtx and emits A2A lifecycle, artifact,
// and status update events.
func (e *AgentExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.Message == nil {
			yield(nil, fmt.Errorf("missing message: %w", a2a.ErrInvalidParams))
			return
		}

		outputMediaType, err := e.negotiateOutputMode(execCtx.AcceptedOutputModes)
		if err != nil {
			yield(nil, err)
			return
		}

		prompt, err := formatPrompt(execCtx)
		if err != nil {
			yield(nil, err)
			return
		}

		runCtx, cancel := context.WithCancel(ctx)
		run := &activeRun{cancel: cancel}
		e.registerRun(execCtx.TaskID, run)
		defer func() {
			cancel()
			e.unregisterRun(execCtx.TaskID, run)
		}()

		if execCtx.StoredTask == nil {
			submitted := a2a.NewSubmittedTask(execCtx, execCtx.Message)
			now := time.Now().UTC().Truncate(time.Millisecond)
			submitted.Status.Timestamp = &now
			if !yield(submitted, nil) {
				return
			}
		}

		if run.canceled.Load() || runCtx.Err() != nil {
			return
		}

		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		unlock := e.contextMap.LockContext(execCtx.ContextID)
		defer unlock()

		if run.canceled.Load() || runCtx.Err() != nil {
			return
		}

		// Isolate Printer and State per execution so concurrent tasks never race
		// on indentation or mutate shared tool/skill maps.
		execPrinter := printer.New(runCtx, io.Discard)
		defer execPrinter.Flush()

		execState := &state.State{
			Printer:     execPrinter,
			Settings:    e.runtime.Settings,
			Llm:         e.runtime.Model,
			ToolsByName: e.toolsByName,
			Skills:      e.runtime.Skills,
			A2a:         e.runtime.A2aClients,
			CtxStore:    e.runtime.CtxStore,
		}
		inputReq := &inputRequest{}
		genCtx := state.WithState(runCtx, execState)
		genCtx = withInputRequest(genCtx, inputReq)

		prevAgCtxID := e.contextMap.Get(execCtx.ContextID)
		nextAgCtxID, eventStream := core.GenerateStream(
			genCtx,
			e.runtime.Model,
			e.tools,
			e.instructions,
			prevAgCtxID,
			prompt,
		)

		drainRemaining := func() {
			cancel()
			go func() {
				for range eventStream {
				}
			}()
		}

		streamText := outputMediaType != "application/json"
		var (
			fullText     strings.Builder
			pendingChunk string
			artifactID   a2a.ArtifactID
			lastErr      string
			inSummary    bool
		)

		emitTextChunk := func(chunk string, last bool) bool {
			part := &a2a.Part{
				Content:   a2a.Text(chunk),
				MediaType: outputMediaType,
			}
			var ev *a2a.TaskArtifactUpdateEvent
			if artifactID == "" {
				ev = a2a.NewArtifactEvent(execCtx, part)
				ev.Artifact.Name = defaultArtifactName
				artifactID = ev.Artifact.ID
			} else {
				ev = a2a.NewArtifactUpdateEvent(execCtx, artifactID, part)
			}
			ev.LastChunk = last
			return yield(ev, nil)
		}

		for rawEv := range eventStream {
			if run.canceled.Load() {
				drainRemaining()
				return
			}
			switch ev := rawEv.(type) {
			case *models.Log:
				if strings.HasPrefix(ev.Text, "Finished") {
					inSummary = false
				} else {
					inSummary = true
				}
			case *models.OutputText:
				if inSummary || ev.Text == "" {
					continue
				}
				fullText.WriteString(ev.Text)
				if streamText {
					if pendingChunk != "" {
						if !emitTextChunk(pendingChunk, false) {
							drainRemaining()
							return
						}
					}
					pendingChunk = ev.Text
				}
			case *models.ToolCalls:
				// Emit a lightweight working heartbeat (with nil message so
				// task.History is not polluted) to keep long tool loops alive
				// under inactivity timeouts.
				if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
					drainRemaining()
					return
				}
			case *models.OutputErr:
				lastErr = ev.Err
			}
		}

		// If Cancel() was invoked, Cancel owns emitting TaskStateCanceled.
		if run.canceled.Load() {
			return
		}
		if ctx.Err() != nil {
			// Parent execution context canceled (e.g. inactivity timeout or shutdown).
			return
		}

		if err := e.contextMap.Set(execCtx.ContextID, nextAgCtxID); err != nil {
			log.Warnf("a2aserver: failed to persist context mapping %s -> %s: %v", execCtx.ContextID, nextAgCtxID, err)
		}

		if lastErr != "" {
			if streamText && pendingChunk != "" {
				if !emitTextChunk(pendingChunk, true) {
					return
				}
			}
			errMsg := a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart(lastErr))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
			return
		}

		if requested, inputPrompt, inputMeta := inputReq.get(); requested {
			if streamText && pendingChunk != "" {
				if !emitTextChunk(pendingChunk, true) {
					return
				}
			} else if !streamText && fullText.Len() > 0 {
				if !emitJSONArtifact(execCtx, fullText.String(), yield) {
					return
				}
			}
			statusPart := &a2a.Part{
				Content:   a2a.Text(inputPrompt),
				MediaType: defaultOutputMimeType,
			}
			if outputMediaType == "application/json" {
				payload := map[string]any{"prompt": inputPrompt}
				if len(inputMeta) > 0 {
					payload["metadata"] = inputMeta
				}
				statusPart = &a2a.Part{
					Content:   a2a.Data{Value: payload},
					MediaType: "application/json",
				}
			}
			msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, statusPart)
			if len(inputMeta) > 0 {
				msg.Metadata = inputMeta
			}
			statusEv := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateInputRequired, msg)
			if len(inputMeta) > 0 {
				statusEv.Metadata = inputMeta
			}
			yield(statusEv, nil)
			return
		}

		if streamText {
			if pendingChunk != "" {
				if !emitTextChunk(pendingChunk, true) {
					return
				}
			}
		} else if fullText.Len() > 0 {
			if !emitJSONArtifact(execCtx, fullText.String(), yield) {
				return
			}
		}

		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

// Cancel stops any active execution for execCtx.TaskID and emits TaskStateCanceled.
func (e *AgentExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			yield(nil, a2a.ErrTaskNotFound)
			return
		}
		e.cancelRun(execCtx.TaskID)
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// Cleanup implements a2asrv.AgentExecutionCleaner.
func (e *AgentExecutor) Cleanup(_ context.Context, execCtx *a2asrv.ExecutorContext, _ a2a.SendMessageResult, _ error) {
	e.mu.Lock()
	if run, ok := e.active[execCtx.TaskID]; ok {
		run.cancel()
		delete(e.active, execCtx.TaskID)
	}
	e.mu.Unlock()
}

func (e *AgentExecutor) registerRun(taskID a2a.TaskID, run *activeRun) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[taskID] = run
}

func (e *AgentExecutor) unregisterRun(taskID a2a.TaskID, run *activeRun) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cur, ok := e.active[taskID]; ok && cur == run {
		delete(e.active, taskID)
	}
}

func (e *AgentExecutor) cancelRun(taskID a2a.TaskID) {
	e.mu.Lock()
	run := e.active[taskID]
	e.mu.Unlock()
	if run != nil {
		run.canceled.Store(true)
		run.cancel()
	}
}

func (e *AgentExecutor) negotiateOutputMode(accepted []string) (string, error) {
	if len(accepted) == 0 {
		return e.outputModes[0], nil
	}

	parsedAccepted := make([]string, 0, len(accepted))
	for _, raw := range accepted {
		mt, _, err := mime.ParseMediaType(raw)
		if err != nil {
			return "", fmt.Errorf("invalid accepted output mode %q: %w", raw, a2a.ErrInvalidParams)
		}
		parsedAccepted = append(parsedAccepted, mt)
	}

	// Prefer the client's order when matching against our supported modes.
	for _, want := range parsedAccepted {
		for _, sup := range e.outputModes {
			if mediaTypeMatches(want, sup) {
				return sup, nil
			}
		}
	}
	return "", fmt.Errorf("none of the accepted output modes %v are supported (supported: %v): %w", accepted, e.outputModes, a2a.ErrUnsupportedContentType)
}

func mediaTypeMatches(pattern, candidate string) bool {
	if pattern == "*/*" || pattern == candidate {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
		return strings.HasPrefix(candidate, prefix+"/")
	}
	return false
}

func emitJSONArtifact(execCtx *a2asrv.ExecutorContext, text string, yield func(a2a.Event, error) bool) bool {
	var payload any
	trimmed := strings.TrimSpace(text)
	if trimmed != "" && stdjson.Valid([]byte(trimmed)) {
		if err := stdjson.Unmarshal([]byte(trimmed), &payload); err != nil {
			payload = map[string]any{"text": text}
		}
	} else {
		payload = map[string]any{"text": text}
	}
	part := &a2a.Part{
		Content:   a2a.Data{Value: payload},
		MediaType: "application/json",
	}
	ev := a2a.NewArtifactEvent(execCtx, part)
	ev.Artifact.Name = defaultArtifactName
	ev.LastChunk = true
	return yield(ev, nil)
}

func formatPrompt(execCtx *a2asrv.ExecutorContext) (string, error) {
	var sections []string

	if len(execCtx.RelatedTasks) > 0 {
		sections = append(sections, formatRelatedTasks(execCtx.RelatedTasks))
	}

	msgText, err := formatMessageParts(execCtx.Message.Parts)
	if err != nil {
		return "", err
	}
	if msgText != "" {
		sections = append(sections, msgText)
	}

	if len(execCtx.Message.Metadata) > 0 {
		if bs, err := stdjson.Marshal(execCtx.Message.Metadata); err == nil {
			sections = append(sections, "[Message Metadata]\n"+string(bs))
		}
	}
	if len(execCtx.Metadata) > 0 {
		if bs, err := stdjson.Marshal(execCtx.Metadata); err == nil {
			sections = append(sections, "[Request Metadata]\n"+string(bs))
		}
	}

	if len(sections) == 0 {
		return "", fmt.Errorf("message contains no usable content: %w", a2a.ErrInvalidParams)
	}
	return strings.Join(sections, "\n\n"), nil
}

func formatMessageParts(parts a2a.ContentParts) (string, error) {
	var chunks []string
	for i, p := range parts {
		if p == nil {
			continue
		}
		switch content := p.Content.(type) {
		case a2a.Text:
			chunks = append(chunks, string(content))
		case a2a.Data:
			bs, err := stdjson.MarshalIndent(content.Value, "", "  ")
			if err != nil {
				return "", fmt.Errorf("parts[%d]: failed to encode data part: %w", i, a2a.ErrInvalidParams)
			}
			chunks = append(chunks, formatAnnotatedBlock("Data", p.Filename, p.MediaType, string(bs)))
		case a2a.URL:
			chunks = append(chunks, formatAnnotatedBlock("URL", p.Filename, p.MediaType, string(content)))
		case a2a.Raw:
			chunks = append(chunks, formatRawPart(p.Filename, p.MediaType, []byte(content)))
		default:
			return "", fmt.Errorf("parts[%d]: unsupported content type %T: %w", i, p.Content, a2a.ErrInvalidParams)
		}
	}
	return strings.Join(chunks, "\n\n"), nil
}

func formatAnnotatedBlock(kind, filename, mediaType, body string) string {
	var attrs []string
	if filename != "" {
		attrs = append(attrs, "filename="+filename)
	}
	if mediaType != "" {
		attrs = append(attrs, "mediaType="+mediaType)
	}
	header := "[" + kind + "]"
	if len(attrs) > 0 {
		header = fmt.Sprintf("[%s (%s)]", kind, strings.Join(attrs, ", "))
	}
	return header + "\n" + body
}

func formatRawPart(filename, mediaType string, raw []byte) string {
	if isTextualMediaType(mediaType) && len(raw) <= maxInlineRawBytes && utf8.Valid(raw) {
		return formatAnnotatedBlock("File", filename, mediaType, string(raw))
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	if len(encoded) > maxRawPreviewBase64 {
		encoded = encoded[:maxRawPreviewBase64] + "...(truncated)"
	}
	summary := fmt.Sprintf("binary payload (%d bytes, base64): %s", len(raw), encoded)
	return formatAnnotatedBlock("Binary", filename, mediaType, summary)
}

func isTextualMediaType(mediaType string) bool {
	if mediaType == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return false
	}
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch mt {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript":
		return true
	}
	return false
}

func formatRelatedTasks(tasks []*a2a.Task) string {
	var sb strings.Builder
	sb.WriteString("[Referenced Tasks]")
	for _, t := range tasks {
		if t == nil {
			continue
		}
		fmt.Fprintf(&sb, "\n- Task %s (context=%s, state=%s)", t.ID, t.ContextID, t.Status.State)
		if t.Status.Message != nil {
			if txt, err := formatMessageParts(t.Status.Message.Parts); err == nil && txt != "" {
				fmt.Fprintf(&sb, "\n  Status message: %s", txt)
			}
		}
		for _, art := range t.Artifacts {
			if art == nil {
				continue
			}
			if txt, err := formatMessageParts(art.Parts); err == nil && txt != "" {
				fmt.Fprintf(&sb, "\n  Artifact %s (%s): %s", art.ID, art.Name, txt)
			}
		}
	}
	return sb.String()
}
