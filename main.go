package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/speed2exe/ag/agentsmd"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/readline"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/state"
	"github.com/speed2exe/ag/store"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/a2a"
	"github.com/speed2exe/ag/tool/builtin"
	"github.com/speed2exe/ag/tool/mcp"
	"github.com/speed2exe/ag/tool/skill"
	"github.com/speed2exe/ag/util"
)

func main() {
	ctx := context.Background()
	p := printer.New(ctx, os.Stdout)
	defer p.Flush()
	if err := mainErr(ctx, p); err != nil {
		p.Print(printer.ColorRed, err.Error(), "\n", printer.Reset)
		p.Flush()
		os.Exit(1)
	}
}

func mainErr(ctx context.Context, p *printer.Printer) error {
	settings, err := setting.Init()
	if err != nil {
		return fmt.Errorf("failed to parse arguments: %w. \nUse -h/--help for usage", err)
	}

	if settings.LoggingEnabled {
		if err := log.Init(settings.LogFilePath); err != nil {
			return fmt.Errorf("failed to initialize log: %w", err)
		}
	}

	go removeOldContexts(settings.ContextDir, settings.ContextTtl)

	model, err := llm.New(settings)
	if err != nil {
		return fmt.Errorf("failed to initialize LLM client: %w", err)
	}
	skills := skill.New(settings, p.Log)
	a2aClients := a2a.New(ctx, p.Log, settings.A2aUrls)
	defer a2aClients.Close()
	toolsByName := buildTools(ctx, p, settings, a2aClients)

	ctx = state.WithState(ctx, &state.State{
		Printer:     p,
		Settings:    settings,
		ToolsByName: toolsByName,
		Skills:      skills,
		A2a:         a2aClients,
		Llm:         model,
		CtxStore:    store.NewCtxStore(settings.ContextDir),
	})

	tools := util.ToolSortedListFromMap(toolsByName)
	instructions := buildInstructions(p, settings, skills, a2aClients)
	eventPrinter := llm.NewEventPrinter(p)
	if setting.StdinIsTerm {
		runInteractive(ctx, p, eventPrinter, settings, model, tools, instructions)
	} else {
		processPrompt(ctx, p, eventPrinter, model, tools, instructions, settings.ContextId, settings.UserInput)
	}
	return nil
}

func buildTools(ctx context.Context, p *printer.Printer, s *setting.Settings, a2aClients *a2a.Clients) map[string]*tool.Tool {
	res := map[string]*tool.Tool{
		consts.BashToolName:    builtin.Bash(),
		consts.AgentToolName:   builtin.Agent(),
		consts.LuaToolName:     builtin.Lua(),
		consts.CompactToolName: builtin.Compact(),
	}
	addTools := func(tools []*tool.Tool) {
		for _, t := range tools {
			if _, exists := res[t.Name]; exists {
				p.Log(fmt.Sprintf("warning: tool %s already exists, skipping\n", t.Name))
				continue
			}
			res[t.Name] = t
		}
	}
	addTools(mcp.LoadTools(ctx, p.Log, s.McpConfPaths))
	addTools(a2aClients.Tools())
	return res
}

func runInteractive(
	ctx context.Context, p *printer.Printer, eventPrinter *llm.EventPrinter, settings *setting.Settings,
	model llm.Model, tools []*tool.Tool, instructions []string,
) {
	ctxId := settings.ContextId

	// Handle session key.
	if settings.SessionKey != "" {
		ctxIdBySessionKey := map[string]string{}
		if loadJSONFile(settings.SessionPath, "session key", &ctxIdBySessionKey) {
			if sessCtxId, exists := ctxIdBySessionKey[settings.SessionKey]; exists {
				ctxId = sessCtxId
			} else {
				log.Warnf("session key %s not found in session file %s", settings.SessionKey, settings.SessionPath)
			}
		}
		defer func() {
			ctxIdBySessionKey[settings.SessionKey] = ctxId
			saveJSONFile(settings.SessionPath, "session key", ctxIdBySessionKey)
		}()
	}

	// Load history prompts.
	var historyPrompts []string
	if loadJSONFile(settings.HistoryFilePath, "history", &historyPrompts) {
		for _, prompt := range historyPrompts {
			readline.AddHistory(prompt)
		}
	}
	defer func() {
		if len(historyPrompts) > settings.HistoryMaxCount {
			historyPrompts = historyPrompts[len(historyPrompts)-settings.HistoryMaxCount:]
		}
		saveJSONFile(settings.HistoryFilePath, "history", historyPrompts)
	}()

	for {
		p.Flush() // flush before blocking for userPrompt to ensure all output is printed
		userPrompt, err := getUserPrompt()
		if errors.Is(err, io.EOF) {
			return
		}
		historyPrompts = append(historyPrompts, userPrompt)
		readline.AddHistory(userPrompt)

		ctxId = processPrompt(ctx, p, eventPrinter, model, tools, instructions, ctxId, userPrompt)
	}
}

func loadJSONFile(path, label string, dst any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Warnf("failed to read %s file %s: %v", label, path, err)
		return false
	}
	if err := json.Unmarshal(data, dst); err != nil {
		log.Errorf("failed to unmarshal %s file %s: %v", label, path, err)
		return false
	}
	return true
}

func saveJSONFile(path, label string, src any) {
	data, err := json.Marshal(src)
	if err != nil {
		log.Errorf("failed to marshal %s file %s: %v", label, path, err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Errorf("failed to write %s file %s: %v", label, path, err)
	}
}

func processPrompt(
	ctx context.Context,
	p *printer.Printer,
	eventPrinter *llm.EventPrinter,
	model llm.Model,
	tools []*tool.Tool,
	instructions []string,
	contextId string,
	userPrompt string,
) string {
	// Handle user interrupt (Ctrl+C).
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	nextCtxId, eventStream := core.GenerateStream(ctx, model, tools, instructions, contextId, userPrompt)
	util.StreamActions(eventStream, eventPrinter.Print)
	eventPrinter.FinishCurrentOutput()
	p.Print(printer.ColorPurple, fmt.Sprintf("--context-id %s\n", nextCtxId), printer.Reset)
	return nextCtxId
}

func getUserPrompt() (string, error) {
	var sb strings.Builder
	for {
		line, err := readline.ReadLine("> ")
		if err != nil {
			return "", fmt.Errorf("readline error: %w", err)
		}
		if line == "" {
			if sb.Len() > 0 {
				return sb.String(), nil
			}
			continue
		}
		if before, ok := strings.CutSuffix(line, "\\"); ok { // line continuation with trailing backslash
			sb.WriteString(before)
			continue
		}
		sb.WriteString(line)
		return sb.String(), nil
	}
}

func buildInstructions(p *printer.Printer, settings *setting.Settings, skills *skill.Skills, a2aClients *a2a.Clients) []string {
	var instructions []string
	if content := loadInstructionFile(p, settings.InstructionPath); content != "" {
		instructions = append(instructions, content)
	}
	if s := agentsmd.Load(p.Log, "."); s != "" {
		instructions = append(instructions, s)
	}
	instructions = append(instructions, skills.BuildInstructions()...)
	if s := a2aClients.BuildInstructions(); s != "" {
		instructions = append(instructions, s)
	}
	return instructions
}

func loadInstructionFile(p *printer.Printer, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Warnf("load instructions from %s error: %s", path, err.Error())
		return ""
	}
	p.Log(fmt.Sprintf("loaded %s\n", path))
	return strings.TrimSpace(string(data))
}

func removeOldContexts(contextDir string, contextTtl time.Duration) {
	deadline := time.Now().Add(-contextTtl)
	files, err := os.ReadDir(contextDir)
	if err != nil {
		log.Warnf("failed to remove old contexts: failed to read context dir %s: %v", contextDir, err)
		return
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		info, err := file.Info()
		if err != nil {
			log.Warnf("failed to get info for context folder %s: %v", file.Name(), err)
			continue
		}
		if info.ModTime().Before(deadline) {
			path := filepath.Join(contextDir, file.Name())
			if err := os.RemoveAll(path); err != nil {
				log.Warnf("failed to remove old context folder %s: %v", path, err)
			} else {
				log.Infof("removed old context folder %s", path)
			}
		}
	}
}
