package llm

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/speed2exe/ag/consts"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/models"
	"github.com/speed2exe/ag/printer"
	"github.com/speed2exe/ag/util"

	"encoding/json/v2"
	"github.com/mattn/go-runewidth"
)

type EventType int

const (
	EventTypeUninitialized EventType = iota
	EventTypeInput
	EventTypeOutput
	EventTypeOthers
)

type OutputType int

const (
	OutputTypeUninitialized OutputType = iota
	OutputTypeText
	OuputTypeThought
	OutputTypeFnCall
	OutputTypeFnArgs
	OutputTypeErr
)

type EventPrinter struct {
	printer *printer.Printer

	currentEventType  EventType
	currentOutputType OutputType

	textBuf          strings.Builder
	textNewLineCount int
	terminalWidth    int
	currentWidth     int

	toolCount                  int
	toolCallName               string
	bashCommandToolCallJsonBuf bytes.Buffer

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
	// if already spinning, do nothing
	if ep.isSpinning {
		return
	}

	ep.printer.Flush()
	ep.stopSpin = make(chan struct{})
	ep.spinDone = make(chan struct{})
	ep.isSpinning = true

	timeNow := time.Now()
	go func() {
		defer close(ep.spinDone)
		for {
			select {
			case <-ep.stopSpin:
				ep.printer.Print(printer.ClearLine, printer.ColorYellow, fmt.Sprintf("\r[Elapsed: %s]", time.Since(timeNow).Truncate(time.Millisecond)), printer.Reset, "\n")
				ep.isSpinning = false
				return
			case <-time.After(177 * time.Millisecond):
				ep.printer.Print(printer.ClearLine, printer.ColorYellow, fmt.Sprintf("\r[Elapsed: %s]", time.Since(timeNow).Truncate(time.Millisecond)), printer.Reset)
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

	previousEventType := ep.currentEventType

	// finish output event if the new event is not output, or the output type is changed
	switch event.(type) {
	case models.Output:
		ep.currentEventType = EventTypeOutput
	default:
		if previousEventType == EventTypeOutput {
			ep.FinishCurrentOutput()
		}
		switch event.(type) {
		case models.Input:
			ep.currentEventType = EventTypeInput
		default:
			ep.currentEventType = EventTypeOthers
		}
	}

	switch e := event.(type) {
	case *models.Spin:
		ep.spin()
	case *models.Log:
		ep.printer.Print(printer.BgYellow, " ", printer.Reset, " ", printer.ColorYellow, e.Text, printer.Reset, "\n")
		return
	case *models.TokenUsage:
		ep.AccumulatePrint(e)
		return
	}

	switch e := event.(type) {
	case models.Input:
		ep.printInput(e)
	case models.Output:
		ep.printOutput(e)
	}
}

func (ep *EventPrinter) AccumulatePrint(usage *models.TokenUsage) {
	// Accumulate the token usage into the total counts
	ep.TotalInputTokens += usage.InputTokens
	ep.TotalInputTokensCached += usage.InputTokensCached
	ep.TotalOutputTokens += usage.OutputTokens
	ep.TotalOutputTokensReasoning += usage.OutputTokensReasoning

	// Print the token usage for this event
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
	previousOutputType := ep.currentOutputType

	var currentOutputType OutputType
	// set event type
	switch event.(type) {
	case *models.OutputUnknown:
	case *models.OutputText:
		currentOutputType = OutputTypeText
	case *models.OutputReasoning:
		currentOutputType = OuputTypeThought
	case *models.OutputFnCall:
		currentOutputType = OutputTypeFnCall
	case *models.OutputFnArgs:
		currentOutputType = OutputTypeFnArgs
	case *models.OutputErr:
		currentOutputType = OutputTypeErr
	default:
		panic(fmt.Sprintf("unsupported event type: %T", event))
	}

	// if there is a change in event type, finish the previous event and start the new event
	if previousOutputType != currentOutputType {
		ep.finishOutput(previousOutputType)
		ep.startEvent(currentOutputType)
		ep.currentOutputType = currentOutputType
	}

	ep.processOutput(event)
}

func (ep *EventPrinter) printToolResult(resultStr string) {
	ep.printer.Print(printer.ColorGreen, "< ", printer.Reset, strings.TrimRightFunc(resultStr, unicode.IsSpace), "\n")
}

func (ep *EventPrinter) printInput(input models.Input) {
	switch in := input.(type) {
	case *models.InputUnknown:
	case *models.ToolCallResult:
		resultStr := in.Result
		switch in.Name {
		case consts.BashToolName:
			var bashOutput models.BashOutput
			if err := json.Unmarshal([]byte(in.Result), &bashOutput); err != nil {
				log.Errorf(fmt.Sprintf("invalid bash_command output JSON: %v", err))
				ep.printToolResult(resultStr)
				break
			}
			sb := strings.Builder{}
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
			if err := json.Unmarshal([]byte(in.Result), &agentOutput); err != nil {
				log.Errorf("invalid agent output JSON: %v", err)
				break
			}
			ep.TotalInputTokens += agentOutput.TotalInputTokens
			ep.TotalInputTokensCached += agentOutput.TotalInputTokensCached
			ep.TotalOutputTokens += agentOutput.TotalOutputTokens
			ep.TotalOutputTokensReasoning += agentOutput.TotalOutputTokensReasoning
		default:
			ep.printToolResult(resultStr)
		}

		// if there are tool calls and every tool is done
		// results will be passed to agent and be processed again, so we need to spin the spinner again
		ep.toolCount--
		if ep.toolCount == 0 {
			ep.spin()
		}
	default:
		panic(fmt.Sprintf("unsupported input type: %T", input))
	}
}

func (ep *EventPrinter) finishOutput(eventType OutputType) {
	switch eventType {
	case OutputTypeUninitialized:
	case OutputTypeText:
		termWidth, _, _ := util.GetTerminalSize()
		leftOffset := ep.printer.GetIndent() * 2
		rendered, err := util.RenderText(termWidth-leftOffset, ep.textBuf.String())
		if err != nil {
			log.Errorf("failed to render markdown: %v", err)
		} else {
			// trim trailing spaces from each line to avoid extra spaces at the end of lines
			lines := strings.Split(rendered, "\n")
			for i, line := range lines {
				lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
			}

			// move the cursor up to the beginning of the text block to overwrite it with the rendered markdown
			ep.printer.PrintWithScratch(func(s *bytes.Buffer) {
				for range ep.textNewLineCount {
					s.WriteString(printer.ClearLine)
					s.WriteString(printer.Up1Line)
				}
				s.WriteString(printer.ClearLine)
				s.WriteString("\r")
				s.WriteString(printer.BgWhite)
				s.WriteString(" ")
				s.WriteString(printer.Reset)
				s.WriteString(" ")
				if len(lines) > 0 {
					s.WriteString(lines[0])
					for _, line := range lines[1:] {
						s.WriteString("\n")
						s.WriteString(line)
					}
				}
			})
			ep.textNewLineCount = 0
		}
		ep.textBuf.Reset()

		ep.printer.Print("\n")
	case OutputTypeFnCall:
		ep.toolCount++
	case OutputTypeFnArgs:
		switch ep.toolCallName {
		case consts.BashToolName:
			var input models.BashInput
			if err := json.Unmarshal(ep.bashCommandToolCallJsonBuf.Bytes(), &input); err != nil {
				log.Errorf("invalid bash_command input JSON: %v", err)
			} else {
				numLineCount := countLinesDownNoNewLine(2 + len(consts.BashToolName) + runewidth.StringWidth(ep.bashCommandToolCallJsonBuf.String()))
				ep.printer.PrintWithScratch(func(s *bytes.Buffer) {
					for range numLineCount {
						s.WriteString(printer.ClearLine)
						s.WriteString(printer.Up1Line)
					}
					s.WriteString(printer.ClearLine)
					s.WriteString("\r")
					s.WriteString(printer.ColorBlue)
					s.WriteString("$ ")
					s.WriteString(printer.Reset)
					s.WriteString(input.Command)
					s.WriteString("\n")
				})
			}
			ep.bashCommandToolCallJsonBuf.Reset()
		default:
			ep.printer.Print(printer.Reset, "\n")
		}
	case OuputTypeThought, OutputTypeErr:
		ep.printer.Print(printer.Reset, "\n")
	default:
		panic(fmt.Sprintf("unsupported event type: %d", eventType))
	}

	ep.currentOutputType = OutputTypeUninitialized
}

func (ep *EventPrinter) startEvent(eventType OutputType) {
	switch eventType {
	case OutputTypeUninitialized:
	case OutputTypeText:
		if width, _, err := util.GetTerminalSize(); err != nil {
			log.Errorf("failed to get terminal size: %v", err)
		} else {
			ep.terminalWidth = width
		}
		ep.printer.Print(printer.BgWhite, " ", printer.Reset, " ")
		ep.currentWidth = 2 + (ep.printer.GetIndent() * 2)
	case OuputTypeThought:
		ep.printer.Print(printer.BgBrightBlack, " ", printer.Reset, " ", printer.ColorBrightBlack)
	case OutputTypeFnCall:
		ep.printer.Print(printer.ColorBlue, "$ ")
	case OutputTypeFnArgs:
		ep.printer.Print(printer.ColorCyan)
	case OutputTypeErr:
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

		// count the number of new lines in the text and update the current width accordingly
		for _, char := range e.Text {
			if char == '\n' {
				ep.textNewLineCount++
				ep.currentWidth = (ep.printer.GetIndent() * 2)
			} else {
				chatWidth := runewidth.RuneWidth(char)
				ep.currentWidth += chatWidth
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
		if ep.toolCallName == consts.BashToolName {
			ep.bashCommandToolCallJsonBuf.WriteString(e.Args)
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
	switch e := event.(type) {
	case *models.OutputErr:
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
