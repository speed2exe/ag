package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/speed2exe/ag/app"
	"github.com/speed2exe/ag/core"
	"github.com/speed2exe/ag/llm"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/readline"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
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

	rt, ctx, err := app.Init(ctx, p, settings)
	if err != nil {
		return err
	}
	defer rt.Close()

	eventPrinter := llm.NewEventPrinter(p)
	if setting.StdinIsTerm {
		runInteractive(ctx, p, eventPrinter, settings, rt.Model, rt.Tools, rt.Instructions)
	} else {
		processPrompt(ctx, p, eventPrinter, rt.Model, rt.Tools, rt.Instructions, settings.ContextId, settings.UserInput)
	}
	return nil
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
