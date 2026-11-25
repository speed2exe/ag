package llm

import (
	"encoding/json/v2"
	"fmt"
	"reflect"
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

type EventPrinter struct {
	printer *printer.Printer

	prevOutputType    reflect.Type
	currentOutputType reflect.Type

	textBuf         strings.Builder
	toolCallName    string
	toolCallArgsBuf strings.Builder

	TotalInputTokens           int
	TotalInputTokensCached     int
	TotalOutputTokens          int
	TotalOutputTokensReasoning int

	stopSpin chan struct{}
	spinDone chan struct{}
}

func NewEventPrinter(p *printer.Printer) *EventPrinter {
	return &EventPrinter{printer: p}
}

func (ep *EventPrinter) spin() {
	if ep.stopSpin != nil {
		return
	}

	ep.printer.Flush()
	stop := make(chan struct{})
	done := make(chan struct{})
	ep.stopSpin = stop
	ep.spinDone = done

	start := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(177 * time.Millisecond)
		defer ticker.Stop()
		elapsed := func() string {
			return fmt.Sprintf("\r[Elapsed: %s]", time.Since(start).Truncate(time.Millisecond))
		}
		for {
			select {
			case <-stop:
				ep.printer.Print(printer.ClearLine, printer.ColorYellow, elapsed(), printer.Reset, "\n")
				return
			case <-ticker.C:
				ep.printer.Print(printer.ClearLine, printer.ColorYellow, elapsed(), printer.Reset)
				ep.printer.Flush()
			}
		}
	}()
}

// stopSpinner signals the spinner goroutine to stop and waits for it to
// finish. Safe to call multiple times; only the first call has any effect.
func (ep *EventPrinter) stopSpinner() {
	if ep.stopSpin == nil {
		return
	}
	close(ep.stopSpin)
	<-ep.spinDone
	ep.stopSpin = nil
}

func (ep *EventPrinter) Print(event models.Event) {
	ep.stopSpinner()

	ep.prevOutputType = ep.currentOutputType
	if _, ok := event.(models.Output); ok {
		ep.currentOutputType = reflect.TypeOf(event)
	} else {
		ep.currentOutputType = nil
	}

	isNew := ep.prevOutputType != ep.currentOutputType
	if isNew {
		ep.finishPrevOutput(false)
	}

	switch e := event.(type) {
	case *models.OutputText:
		if isNew {
			ep.printer.Print(printer.BgWhite, " ", printer.Reset, " ")
		}
		ep.textBuf.WriteString(e.Text)
		ep.printer.Print(e.Text)
	case *models.OutputReasoning:
		if isNew {
			ep.printer.Print(printer.BgBrightBlack, " ", printer.Reset, " ", printer.ColorBrightBlack)
		}
		ep.printer.Print(e.Reasoning)
	case *models.OutputFnCall:
		if isNew {
			ep.printer.Print(printer.ColorBlue, "$ ")
		}
		ep.toolCallName = e.Name
		ep.printer.Print(printer.ColorGreen, e.Name)
	case *models.OutputFnArgs:
		if isNew {
			ep.printer.Print(printer.ColorCyan)
		}
		if ep.toolCallName == consts.BashToolName || ep.toolCallName == consts.LuaToolName {
			ep.toolCallArgsBuf.WriteString(e.Args)
		}
		ep.printer.Print(e.Args)
	case *models.OutputErr:
		if isNew {
			ep.printer.Print(printer.BgRed, " ", printer.Reset, " ", printer.ColorRed)
		}
		ep.printer.Print(e.Err)
	case *models.Spin:
		ep.spin()
	case *models.Log:
		ep.printer.Print(printer.BgYellow, " ", printer.Reset, " ", printer.ColorYellow, e.Text, printer.Reset, "\n")
	case *models.TokenUsage:
		ep.AccumulatePrint(e)
	case *models.ToolCalls:
		for _, toolCall := range e.Items {
			ep.printToolCallResult(toolCall)
		}
		ep.spin()
	default:
		if ep.currentOutputType != nil {
			panic(fmt.Sprintf("unsupported event type: %T", event))
		}
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
	ep.prevOutputType = ep.currentOutputType
	ep.currentOutputType = nil
	ep.finishPrevOutput(true)
}

func (ep *EventPrinter) printToolResult(resultStr string) {
	ep.printer.Print(printer.ColorGreen, "< ", printer.Reset, strings.TrimRightFunc(resultStr, unicode.IsSpace), "\n")
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
			fmt.Fprintf(&sb, "%s %s %s", printer.ColorRed, luaOutput.Stderr, printer.Reset)
		}
		sb.WriteString(luaOutput.Stdout)
		if luaOutput.Result != nil {
			if luaOutput.Stdout != "" {
				sb.WriteByte('\n')
			}
			if result, err := json.Marshal(luaOutput.Result); err != nil {
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
			fmt.Fprintf(&sb, "%s %v %s", printer.BgRed, bashOutput.ExitCode, printer.Reset)
		}
		if bashOutput.Stderr != "" {
			fmt.Fprintf(&sb, "%s %s %s", printer.ColorRed, bashOutput.Stderr, printer.Reset)
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

func (ep *EventPrinter) finishPrevOutput(isEnd bool) {
	// fn call without fn args
	if ep.prevOutputType == reflect.TypeFor[*models.OutputFnCall]() {
		if ep.currentOutputType != reflect.TypeFor[*models.OutputFnArgs]() {
			ep.printer.Print("\n")
		}
	}

	switch ep.prevOutputType {
	case nil:
	case reflect.TypeFor[*models.OutputFnCall]():
		// fn call without fn args
		if isEnd {
			ep.printer.Print("\n")
		}
	case reflect.TypeFor[*models.OutputText]():
		text := ep.textBuf.String()
		ep.textBuf.Reset()
		termWidth := util.GetTerminalWidth()
		leftOffset := ep.printer.GetIndent() * 2
		if rendered, err := util.RenderText(termWidth-leftOffset, text); err != nil {
			log.Errorf("failed to render markdown: %v", err)
		} else {
			ep.printer.Print(clearLines(countTextLines(text, termWidth, leftOffset)), printer.BgWhite, " ", printer.Reset, " ", rendered)
		}
		ep.printer.Print("\n")

	case reflect.TypeFor[*models.OutputFnArgs]():
		ep.finishFnArgs()

	case reflect.TypeFor[*models.OutputReasoning](), reflect.TypeFor[*models.OutputErr]():
		ep.printer.Print(printer.Reset, "\n")

	default:
		panic(fmt.Sprintf("unsupported event type: %v", ep.prevOutputType))
	}
}

func (ep *EventPrinter) finishFnArgs() {
	rawArgs := ep.toolCallArgsBuf.String()
	ep.toolCallArgsBuf.Reset()

	switch ep.toolCallName {
	case consts.BashToolName:
		var input models.BashInput
		if err := json.Unmarshal([]byte(rawArgs), &input); err != nil {
			log.Errorf("invalid bash_command input JSON: %v", err)
			return
		}
		termWidth := util.GetTerminalWidth()
		lines := countWrappedLines(termWidth, 2+len(consts.BashToolName)+runewidth.StringWidth(rawArgs))
		ep.printer.Print(clearLines(lines), printer.ColorBlue, "$ ", printer.Reset,
			util.MustRenderCodeBlock("bash", termWidth, input.Command), "\n")

	case consts.LuaToolName:
		var input models.LuaInput
		if err := json.Unmarshal([]byte(rawArgs), &input); err != nil {
			log.Errorf("invalid lua input JSON: %v", err)
			return
		}
		termWidth := util.GetTerminalWidth()
		lines := countWrappedLines(termWidth, 2+len(consts.LuaToolName)+runewidth.StringWidth(rawArgs))
		ep.printer.Print(clearLines(lines), printer.ColorPurple, "$ ", input.Name, printer.Reset, "\n",
			util.MustRenderCodeBlock("lua", termWidth, input.Script), "\n")

	default:
		ep.printer.Print(printer.Reset, "\n")
	}
}

func clearLines(count int) string {
	return strings.Repeat(printer.ClearLine+printer.Up1Line, count) + printer.ClearLine + "\r"
}

func countTextLines(text string, termWidth, leftOffset int) int {
	lines := 0
	col := 2 + leftOffset
	for _, r := range text {
		if r == '\n' {
			lines++
			col = leftOffset
			continue
		}
		col += runewidth.RuneWidth(r)
		if termWidth > 0 && col >= termWidth {
			lines++
			col = 0
		}
	}
	return lines
}

func countWrappedLines(termWidth, charCount int) int {
	if termWidth <= 0 || charCount <= 0 {
		return 0
	}
	return (charCount - 1) / termWidth
}
