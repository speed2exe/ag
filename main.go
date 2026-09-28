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
	"github.com/speed2exe/ag/tool/builtin"
	"github.com/speed2exe/ag/tool/mcp"
	"github.com/speed2exe/ag/tool/skill"
	"github.com/speed2exe/ag/util"
)

func main() {
	p := printer.New(os.Stdout)
	defer p.Flush()
	if err := mainErr(p); err != nil {
		p.Print(printer.ColorRed, err.Error(), "\n", printer.Reset)
		p.Flush()
		os.Exit(1)
	}
}

func buildTools(ctx context.Context, p *printer.Printer, s *setting.Settings) map[string]*tool.Tool {
	var luaTools []*tool.Tool
	for _, tools := range mcp.NewMcpFromConfig(ctx, p.Log, s.McpConfPaths) {
		luaTools = append(luaTools, tools...)
	}
	return map[string]*tool.Tool{
		consts.BashToolName:  builtin.Bash(),
		consts.AgentToolName: builtin.Agent(),
		consts.LuaToolName:   builtin.Lua(luaTools),
	}
}

func mainErr(p *printer.Printer) error {
	settings, err := setting.Init()
	if err != nil {
		return fmt.Errorf("failed to parse arguments: %w. \nUse -h/--help for usage", err)
	}

	go removeOldContexts(settings.ContextDir, settings.ContextTtl)

	if err := log.Init(logFilePath(settings)); err != nil {
		return fmt.Errorf("failed to initialize log: %w", err)
	}

	ctx := context.Background()

	model, err := llm.New(p.Log, settings)
	if err != nil {
		return fmt.Errorf("failed to initialize LLM client: %w", err)
	}
	skills := skill.New(settings, p.Log)
	toolsByName := buildTools(ctx, p, settings)

	ctx = state.WithState(ctx, &state.State{
		Printer:     p,
		Settings:    settings,
		ToolsByName: toolsByName,
		Skills:      skills,
		Llm:         model,
		CtxStore:    store.NewCtxStore(settings.ContextDir),
	})

	instructions := buildInstructions(p, settings, skills)
	if settings.Interactive {
		runInteractive(ctx, p, llm.NewEventPrinter(p), settings, model, toolsByName, instructions)
	} else {
		processPrompt(ctx, p, llm.NewEventPrinter(p), model, toolsByName, instructions, settings.ContextId, settings.UserInput.String())
	}
	return nil
}

func runInteractive(
	ctx context.Context, p *printer.Printer, eventPrinter *llm.EventPrinter, settings *setting.Settings,
	model llm.Model, toolsByName map[string]*tool.Tool, instructions []string,
) {
	ctxId := settings.ContextId

	// handle session key
	if settings.SessionKey != "" {
		ctxIdBySessionKey := map[string]string{}
		if data, err := os.ReadFile(settings.SessionPath); err != nil {
			log.Errorf("failed to read session key file %s: %v", settings.SessionPath, err)
		} else if err := json.Unmarshal(data, &ctxIdBySessionKey); err != nil {
			log.Errorf("failed to unmarshal session key file %s: %v", settings.SessionPath, err)
		} else if sessCtxId, exists := ctxIdBySessionKey[settings.SessionKey]; !exists {
			log.Warnf("session key %s not found in session file %s", settings.SessionKey, settings.SessionPath)
		} else {
			ctxId = sessCtxId
		}

		defer func() {
			ctxIdBySessionKey[settings.SessionKey] = ctxId
			if data, err := json.Marshal(ctxIdBySessionKey); err != nil {
				log.Errorf("failed to marshal session key file %s: %v", settings.SessionPath, err)
			} else if err := os.WriteFile(settings.SessionPath, data, 0644); err != nil {
				log.Errorf("failed to write session key file %s: %v", settings.SessionPath, err)
			}
		}()
	}

	// If stdin was piped (e.g. `echo hi | agent -i`), settings.UserInput
	// holds that piped text; process it as the first turn before dropping
	// into the terminal-driven ReadLine loop for subsequent turns.
	pendingPrompt := strings.TrimSpace(settings.UserInput.String())
	if pendingPrompt != "" {
		ctxId = processPrompt(ctx, p, eventPrinter, model, toolsByName, instructions, ctxId, pendingPrompt)
	}

	// load history prompts
	var historyPrompts []string
	if hist, err := os.ReadFile(settings.HistoryFilePath); err != nil {
		log.Warnf("failed to load history file %s: %v", settings.HistoryFilePath, err)
	} else if err := json.Unmarshal(hist, &historyPrompts); err != nil {
		log.Errorf("failed to unmarshal history file %s: %v", settings.HistoryFilePath, err)
	} else {
		AddHistory(historyPrompts...)
	}
	defer func() { // write back history prompts to file on exit
		// keep only the last N prompts in history file to avoid it growing too large
		maxCount := settings.HistoryMaxCount
		if len(historyPrompts) > maxCount {
			historyPrompts = historyPrompts[len(historyPrompts)-maxCount:]
		}

		if data, err := json.Marshal(historyPrompts); err != nil {
			log.Errorf("failed to marshal history prompts: %v", err)
		} else if err := os.WriteFile(settings.HistoryFilePath, data, 0644); err != nil {
			log.Errorf("failed to write history file %s: %v", settings.HistoryFilePath, err)
		}
	}()

	for {
		p.Flush() // flush before blocking for userPrompt to ensure all output is printed
		userPrompt, err := getUserPrompt()
		if errors.Is(err, io.EOF) {
			return
		}
		historyPrompts = append(historyPrompts, userPrompt)
		AddHistory(userPrompt)

		ctxId = processPrompt(ctx, p, eventPrinter, model, toolsByName, instructions, ctxId, userPrompt)
	}
}

func processPrompt(
	ctx context.Context,
	p *printer.Printer,
	eventPrinter *llm.EventPrinter,
	model llm.Model,
	toolsByName map[string]*tool.Tool,
	instructions []string,
	contextId string,
	userPrompt string,
) string {
	// handle user interrupt (Ctrl+C)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	nextCtxId, eventStream := core.GenerateStream(ctx, model, util.ToolSortedListFromMap(toolsByName), instructions, contextId, userPrompt)
	util.StreamActions(eventStream, eventPrinter.Print)
	eventPrinter.FinishCurrentOutput()
	p.Print(printer.ColorPurple, fmt.Sprintf("--context-id %s\n", nextCtxId), printer.Reset)
	return nextCtxId
}

func getUserPrompt() (string, error) {
	sb := strings.Builder{}
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
		if line[len(line)-1] == '\\' { // line continuation with trailing backslash
			sb.WriteString(line[:len(line)-1]) // drop the backslash
			continue
		}
		sb.WriteString(line)
		break
	}
	return sb.String(), nil
}

func AddHistory(prompt ...string) {
	for _, p := range prompt {
		readline.AddHistory(p)
	}
}

func buildInstructions(p *printer.Printer, settings *setting.Settings, skills *skill.Skills) []string {
	instructions := []string{}
	// instructions = append(instructions, fmt.Sprintf("env: %s", strings.Join(os.Environ(), "\n")))
	// instructions = append(instructions, fmt.Sprintf("args: %s", strings.Join(os.Args, "\n")))
	if content := loadInstructionFile(p, settings.InstructionPath); content != "" {
		instructions = append(instructions, content)
	}
	if s := agentsmd.Load(p.Log, "."); s != "" {
		instructions = append(instructions, s)
	}
	instructions = append(instructions, skill.BuildPreloadSkillInstructions(skills.PreloadSkillByFilePath)...)
	if s := skill.BuildSkillInstructions(skills.SkillByFilePath); s != "" {
		instructions = append(instructions, s)
	}
	return instructions
}

func loadInstructionFile(p *printer.Printer, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Warnf("load instructions from %s error: %w", path, err)
		return ""
	}
	p.Print(printer.ColorYellow, fmt.Sprintf("loaded %s\n", path), printer.Reset)
	return strings.TrimSpace(string(data))
}

func logFilePath(s *setting.Settings) *string {
	if s.LoggingEnabled {
		return &s.LogFilePath
	}
	return nil
}

func removeOldContexts(contextDir string, contextTtl time.Duration) {
	if err := removeOldContextsError(contextDir, contextTtl); err != nil {
		log.Warnf("failed to remove old contexts: %v", err)
	}
}

func removeOldContextsError(contextDir string, contextTtl time.Duration) error {
	deadline := time.Now().Add(-contextTtl)

	files, err := os.ReadDir(contextDir)
	if err != nil {
		return fmt.Errorf("failed to read context dir %s: %w", contextDir, err)
	}

	for _, file := range files {
		if !file.IsDir() {
			// context folders should be directories, skip files
			continue
		}
		info, err := file.Info() // get file info to check mod time
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

	return nil
}
