package llm

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/util"
)

type outputType int

const (
	outputTypeNone outputType = iota
	outputTypeText
	outputTypeThought
	outputTypeFnCall
	outputTypeFnArgs
	outputTypeErr
)

type EventPrinter struct {
	printer *printer.Printer

	currentOutputType outputType

	textBuf          strings.Builder
	textNewLineCount int
	terminalWidth    int
	currentWidth     int

	toolCallName    string
	toolCallArgsBuf bytes.Buffer

	TotalInputTokens           int
	TotalInputTokensCached     int
	TotalOutputTokens          int
	TotalOutputTokensReasoning int

	stopSpin   chan struct{}
	spinDone   chan struct{}
	isSpinning bool
}

func NewEventPrinter(p *printer.Printer) *EventPrinter {
	return &EventPrinter{printer: p}
}

func (ep *EventPrinter) spin() {
	if ep.isSpinning {
		return
	}

	ep.printer.Flush()
	ep.stopSpin = make(chan struct{})
	ep.spinDone = make(chan struct{})
	ep.isSpinning = true

	start := time.Now()
	go func() {
		defer close(ep.spinDone)
		ticker := time.NewTicker(177 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ep.stopSpin:
				ep.printer.Print(printer.ClearLine, printer.ColorYellow, fmt.Sprintf("\r[Elapsed: %s]", time.Since(start).Truncate(time.Millisecond)), printer.Reset, "\n")
				ep.isSpinning = false
				return
			case <-ticker.C:
				ep.printer.Print(printer.ClearLine, printer.ColorYellow, fmt.Sprintf("\r[Elapsed: %s]", time.Since(start).Truncate(time.Millisecond)), printer.Reset)
				ep.printer.Flush()
			}
		}
	}()
}

// stopSpinner signals the spinner goroutine to stop and waits for it to
// finish. Safe to call multiple times; only the first call has any effect.
func (ep *EventPrinter) stopSpinner() {
	if !ep.isSpinning {
		return
	}
	close(ep.stopSpin)
	<-ep.spinDone
}

func (ep *EventPrinter) Print(event models.Event) {
	ep.stopSpinner()

	if out, ok := event.(models.Output); ok {
		ep.printOutput(out)
		return
	}

	ep.finishOutput(ep.currentOutputType)

	switch e := event.(type) {
	case *models.Spin:
		ep.spin()
	case *models.Log:
		ep.printer.Print(printer.BgYellow, " ", printer.Reset, " ", printer.ColorYellow, e.Text, printer.Reset, "\n")
	case *models.TokenUsage:
		ep.AccumulatePrint(e)
	case models.Input:
		ep.printInput(e)
	}
}

func (ep *EventPrinter) AccumulatePrint(usage *models.TokenUsage) {
	ep.TotalInputTokens += usage.InputTokens
	ep.TotalInputTokensCached += usage.InputTokensCached
	ep.TotalOutputTokens += usage.OutputTokens
	ep.TotalOutputTokensReasoning += usage.OutputTokensReasoning

	ep.printer.Print(printer.BgYellow, " ", printer.Reset, " ", printer.ColorYellow,
		fmt.Sprintf("tokens in: %d (cached: %d) out: %d (reasoning: %d) | total in: %d (cached: %d) out: %d (reasoning: %d)\n",
			usage.InputTokens, usage.InputTokensCached, usage.OutputTokens, usage.OutputTokensReasoning,
			ep.TotalInputTokens, ep.TotalInputTokensCached, ep.TotalOutputTokens, ep.TotalOutputTokensReasoning),
		printer.Reset,
	)
}

func (ep *EventPrinter) FinishCurrentOutput() {
	ep.stopSpinner()
	ep.finishOutput(ep.currentOutputType)
}

func (ep *EventPrinter) printOutput(event models.Output) {
	var nextType outputType
	switch event.(type) {
	case *models.OutputUnknown:
	case *models.OutputText:
		nextType = outputTypeText
	case *models.OutputReasoning:
		nextType = outputTypeThought
	case *models.OutputFnCall:
		nextType = outputTypeFnCall
	case *models.OutputFnArgs:
		nextType = outputTypeFnArgs
	case *models.OutputErr:
		nextType = outputTypeErr
	default:
		panic(fmt.Sprintf("unsupported event type: %T", event))
	}

	if ep.currentOutputType != nextType {
		ep.finishOutput(ep.currentOutputType)
		ep.startEvent(nextType)
		ep.currentOutputType = nextType
	}

	ep.processOutput(event)
}

func (ep *EventPrinter) printToolResult(resultStr string) {
	ep.printer.Print(printer.ColorGreen, "< ", printer.Reset, strings.TrimRightFunc(resultStr, unicode.IsSpace), "\n")
}

func (ep *EventPrinter) printInput(input models.Input) {
	switch in := input.(type) {
	case *models.InputUnknown:
	case *models.ToolCalls:
		for _, toolCall := range in.Items {
			ep.printToolCallResult(toolCall)
		}
		ep.spin()
	default:
		panic(fmt.Sprintf("unsupported input type: %T", input))
	}
}

func (ep *EventPrinter) printToolCallResult(toolCall *models.ToolCall) {
	switch toolCall.Name {
	case consts.LuaToolName:
		var luaOutput models.LuaOutput
		if err := json.Unmarshal([]byte(toolCall.Result), &luaOutput); err != nil {
			log.Errorf("invalid lua output JSON: %v", err)
			ep.printToolResult(toolCall.Result)
			return
		}
		var sb strings.Builder
		if luaOutput.Stderr != "" {
			sb.WriteString(printer.ColorRed)
			fmt.Fprintf(&sb, " %s ", luaOutput.Stderr)
			sb.WriteString(printer.Reset)
		}
		sb.WriteString(luaOutput.Stdout)
		if luaOutput.Result != nil {
			if luaOutput.Stdout != "" {
				sb.WriteByte('\n')
			}
			result, err := json.Marshal(luaOutput.Result)
			if err != nil {
				log.Errorf("failed to marshal Lua result: %v", err)
			} else {
				sb.Write(result)
			}
		}
		ep.printToolResult(sb.String())

	case consts.BashToolName:
		var bashOutput models.BashOutput
		if err := json.Unmarshal([]byte(toolCall.Result), &bashOutput); err != nil {
			log.Errorf("invalid bash_command output JSON: %v", err)
			ep.printToolResult(toolCall.Result)
			return
		}
		var sb strings.Builder
		if bashOutput.ExitCode != 0 {
			sb.WriteString(printer.BgRed)
			fmt.Fprintf(&sb, " %v ", bashOutput.ExitCode)
			sb.WriteString(printer.Reset)
		}
		if bashOutput.Stderr != "" {
			sb.WriteString(printer.ColorRed)
			fmt.Fprintf(&sb, " %s ", bashOutput.Stderr)
			sb.WriteString(printer.Reset)
		}
		sb.WriteString(bashOutput.Stdout)
		ep.printToolResult(sb.String())

	case consts.AgentToolName:
		var agentOutput models.AgentOutput
		if err := json.Unmarshal([]byte(toolCall.Result), &agentOutput); err != nil {
			log.Errorf("invalid agent output JSON: %v", err)
			return
		}
		ep.TotalInputTokens += agentOutput.TotalInputTokens
		ep.TotalInputTokensCached += agentOutput.TotalInputTokensCached
		ep.TotalOutputTokens += agentOutput.TotalOutputTokens
		ep.TotalOutputTokensReasoning += agentOutput.TotalOutputTokensReasoning

	default:
		ep.printToolResult(toolCall.Result)
	}
}

func (ep *EventPrinter) finishOutput(eventType outputType) {
	switch eventType {
	case outputTypeNone, outputTypeFnCall:
	case outputTypeText:
		termWidth, _, _ := util.GetTerminalSize()
		leftOffset := ep.printer.GetIndent() * 2
		rendered, err := util.RenderText(termWidth-leftOffset, ep.textBuf.String())
		if err != nil {
			log.Errorf("failed to render markdown: %v", err)
		} else {
			ep.printer.PrintWithScratch(func(s *bytes.Buffer) {
				clearLines(s, ep.textNewLineCount)
				s.WriteString(printer.BgWhite)
				s.WriteString(" ")
				s.WriteString(printer.Reset)
				s.WriteString(" ")
				s.WriteString(rendered)
			})
			ep.textNewLineCount = 0
		}
		ep.textBuf.Reset()
		ep.printer.Print("\n")

	case outputTypeFnArgs:
		ep.finishFnArgs()

	case outputTypeThought, outputTypeErr:
		ep.printer.Print(printer.Reset, "\n")

	default:
		panic(fmt.Sprintf("unsupported event type: %d", eventType))
	}

	ep.currentOutputType = outputTypeNone
}

func (ep *EventPrinter) finishFnArgs() {
	defer ep.toolCallArgsBuf.Reset()

	switch ep.toolCallName {
	case consts.BashToolName:
		var input models.BashInput
		if err := json.Unmarshal(ep.toolCallArgsBuf.Bytes(), &input); err != nil {
			log.Errorf("invalid bash_command input JSON: %v", err)
			return
		}
		termWidth, _, _ := util.GetTerminalSize()
		numLineCount := countLinesDownNoNewLine(2 + len(consts.BashToolName) + runewidth.StringWidth(ep.toolCallArgsBuf.String()))
		ep.printer.PrintWithScratch(func(s *bytes.Buffer) {
			clearLines(s, numLineCount)
			s.WriteString(printer.ColorBlue)
			s.WriteString("$ ")
			s.WriteString(printer.Reset)
			s.WriteString(util.MustRenderCodeBlock("bash", termWidth, input.Command))
			s.WriteString("\n")
		})

	case consts.LuaToolName:
		var input models.LuaInput
		if err := json.Unmarshal(ep.toolCallArgsBuf.Bytes(), &input); err != nil {
			log.Errorf("invalid lua input JSON: %v", err)
			return
		}
		termWidth, _, _ := util.GetTerminalSize()
		numLineCount := countLinesDownNoNewLine(2 + len(consts.LuaToolName) + runewidth.StringWidth(ep.toolCallArgsBuf.String()))
		ep.printer.PrintWithScratch(func(s *bytes.Buffer) {
			clearLines(s, numLineCount)
			s.WriteString(printer.ColorPurple)
			s.WriteString("$ ")
			s.WriteString(input.Name)
			s.WriteString(printer.Reset)
			s.WriteString("\n")
			s.WriteString(util.MustRenderCodeBlock("lua", termWidth, input.Script))
			s.WriteString("\n")
		})

	default:
		ep.printer.Print(printer.Reset, "\n")
	}
}

func clearLines(s *bytes.Buffer, count int) {
	for range count {
		s.WriteString(printer.ClearLine)
		s.WriteString(printer.Up1Line)
	}
	s.WriteString(printer.ClearLine)
	s.WriteString("\r")
}

func (ep *EventPrinter) startEvent(eventType outputType) {
	switch eventType {
	case outputTypeNone:
	case outputTypeText:
		if width, _, err := util.GetTerminalSize(); err != nil {
			log.Errorf("failed to get terminal size: %v", err)
		} else {
			ep.terminalWidth = width
		}
		ep.printer.Print(printer.BgWhite, " ", printer.Reset, " ")
		ep.currentWidth = 2 + (ep.printer.GetIndent() * 2)
	case outputTypeThought:
		ep.printer.Print(printer.BgBrightBlack, " ", printer.Reset, " ", printer.ColorBrightBlack)
	case outputTypeFnCall:
		ep.printer.Print(printer.ColorBlue, "$ ")
	case outputTypeFnArgs:
		ep.printer.Print(printer.ColorCyan)
	case outputTypeErr:
		ep.printer.Print(printer.BgRed, " ", printer.Reset, " ", printer.ColorRed)
	default:
		panic(fmt.Sprintf("unsupported event type: %d", eventType))
	}
}

func (ep *EventPrinter) processOutput(event models.Output) {
	switch e := event.(type) {
	case *models.OutputUnknown:
	case *models.OutputText:
		ep.textBuf.WriteString(e.Text)
		for _, char := range e.Text {
			if char == '\n' {
				ep.textNewLineCount++
				ep.currentWidth = ep.printer.GetIndent() * 2
			} else {
				ep.currentWidth += runewidth.RuneWidth(char)
				if ep.terminalWidth > 0 && ep.currentWidth >= ep.terminalWidth {
					ep.textNewLineCount++
					ep.currentWidth = 0
				}
			}
		}
		ep.printer.Print(e.Text)
	case *models.OutputReasoning:
		ep.printer.Print(e.Reasoning)
	case *models.OutputFnCall:
		ep.toolCallName = e.Name
		ep.printer.Print(printer.ColorGreen, e.Name)
	case *models.OutputFnArgs:
		if ep.toolCallName == consts.BashToolName || ep.toolCallName == consts.LuaToolName {
			ep.toolCallArgsBuf.WriteString(e.Args)
		}
		ep.printer.Print(e.Args)
	case *models.OutputErr:
		ep.printer.Print(e.Err)
	default:
		panic(fmt.Sprintf("unsupported event type: %T", event))
	}
}

type LastOutputError struct {
	Err *models.OutputErr `json:"err,omitempty"`
}

func (le *LastOutputError) Set(event models.Event) {
	if e, ok := event.(*models.OutputErr); ok {
		le.Err = e
	}
}

func countLinesDownNoNewLine(charCount int) int {
	width, _, err := util.GetTerminalSize()
	if err != nil {
		log.Errorf("failed to get terminal size: %v", err)
		return 0
	}
	if width <= 0 {
		log.Errorf("invalid terminal width: %d", width)
		return 0
	}
	return (charCount - 1) / width
}
