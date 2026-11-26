package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/speed2exe/ag/agentsmd"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/printer"
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

// Runtime bundles initialized agent dependencies shared by CLI and server binaries.
type Runtime struct {
	Settings     *setting.Settings
	Model        llm.Model
	Skills       *skill.Skills
	A2aClients   *a2a.Clients
	ToolsByName  map[string]*tool.Tool
	Tools        []*tool.Tool
	CtxStore     *store.CtxStore
	Instructions []string
}

// Init initializes logging, LLM client, skills, outbound A2A clients, tools,
// context store, and instructions, returning a Runtime and a context carrying state.State.
func Init(ctx context.Context, p *printer.Printer, settings *setting.Settings) (*Runtime, context.Context, error) {
	if settings.LoggingEnabled {
		if err := log.Init(settings.LogFilePath); err != nil {
			return nil, ctx, fmt.Errorf("failed to initialize log: %w", err)
		}
	}

	go RemoveOldContexts(settings.ContextDir, settings.ContextTtl)

	model, err := llm.New(settings)
	if err != nil {
		return nil, ctx, fmt.Errorf("failed to initialize LLM client: %w", err)
	}
	skills := skill.New(settings, p.Log)
	a2aClients := a2a.New(ctx, p.Log, settings.A2aUrls)
	toolsByName := BuildTools(ctx, p, settings, a2aClients)
	ctxStore := store.NewCtxStore(settings.ContextDir)

	rt := &Runtime{
		Settings:     settings,
		Model:        model,
		Skills:       skills,
		A2aClients:   a2aClients,
		ToolsByName:  toolsByName,
		Tools:        util.ToolSortedListFromMap(toolsByName),
		CtxStore:     ctxStore,
		Instructions: BuildInstructions(p, settings, skills, a2aClients),
	}

	ctx = state.WithState(ctx, &state.State{
		Printer:     p,
		Settings:    settings,
		ToolsByName: toolsByName,
		Skills:      skills,
		A2a:         a2aClients,
		Llm:         model,
		CtxStore:    ctxStore,
	})
	return rt, ctx, nil
}

// Close releases resources held by the runtime.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	if r.A2aClients != nil {
		r.A2aClients.Close()
	}
}

func BuildTools(ctx context.Context, p *printer.Printer, s *setting.Settings, a2aClients *a2a.Clients) map[string]*tool.Tool {
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

func BuildInstructions(p *printer.Printer, settings *setting.Settings, skills *skill.Skills, a2aClients *a2a.Clients) []string {
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

func RemoveOldContexts(contextDir string, contextTtl time.Duration) {
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
