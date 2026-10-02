// Package printer provides colored, streaming console output for the agent CLI.
// Each Printer wraps the writer supplied to [New].
package printer

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/speed2exe/ag/log"
)

// Printer writes to an underlying writer through a bufio.Writer. Callers
// are responsible for calling Flush at appropriate points (e.g. before
// blocking for input, or on a spinner tick). All methods are safe for
// concurrent use.
type Printer struct {
	mu     sync.Mutex
	output *bufio.Writer
	indent int
}

func New(ctx context.Context, w io.Writer) *Printer {
	p := &Printer{output: bufio.NewWriter(w)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(177 * time.Millisecond):
				p.Flush()
			}
		}
	}()
	return p
}

func (p *Printer) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeIndented(data)
}

// writeIndented writes data, inserting indentation after every line break.
// It must be called with p.mu held.
func (p *Printer) writeIndented(data []byte) (int, error) {
	if p.indent == 0 {
		return p.output.Write(data)
	}
	total := 0
	for len(data) > 0 {
		idx := bytes.IndexAny(data, "\r\n")
		if idx == -1 {
			n, err := p.output.Write(data)
			return total + n, err
		}
		n, err := p.output.Write(data[:idx+1])
		total += n
		if err != nil {
			return total, err
		}
		p.output.Write(bytes.Repeat([]byte("  "), p.indent))
		data = data[idx+1:]
	}
	return total, nil
}

func (p *Printer) Indent() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.indent++
}

func (p *Printer) UnIndent() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.indent > 0 {
		p.indent--
	}
}

func (p *Printer) GetIndent() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.indent
}

func (p *Printer) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.output.Flush(); err != nil {
		log.Errorf("failed to flush output stream: %v", err)
	}
}

func (p *Printer) Log(str string) {
	p.Print(ColorYellow, str, Reset)
}

// Print writes strs to the configured writer with no added color or newline.
func (p *Printer) Print(strs ...string) {
	p.Write([]byte(strings.Join(strs, "")))
}

// PrintWithScratch builds output into a scratch buffer and writes it
// atomically, so the whole rendered block is written in one Write call.
func (p *Printer) PrintWithScratch(f func(s *bytes.Buffer)) {
	var buf bytes.Buffer
	f(&buf)
	p.Write(buf.Bytes())
}

const (
	Reset = "\033[0m"

	ColorBlack  = "\033[30m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"

	ColorBrightBlack  = "\033[90m"
	ColorBrightRed    = "\033[91m"
	ColorBrightGreen  = "\033[92m"
	ColorBrightYellow = "\033[93m"
	ColorBrightBlue   = "\033[94m"
	ColorBrightPurple = "\033[95m"
	ColorBrightCyan   = "\033[96m"
	ColorBrightWhite  = "\033[97m"

	BgBlack  = "\033[40m"
	BgRed    = "\033[41m"
	BgGreen  = "\033[42m"
	BgYellow = "\033[43m"
	BgBlue   = "\033[44m"
	BgPurple = "\033[45m"
	BgCyan   = "\033[46m"
	BgWhite  = "\033[47m"

	BgBrightBlack  = "\033[100m"
	BgBrightRed    = "\033[101m"
	BgBrightGreen  = "\033[102m"
	BgBrightYellow = "\033[103m"
	BgBrightBlue   = "\033[104m"
	BgBrightPurple = "\033[105m"
	BgBrightCyan   = "\033[106m"
	BgBrightWhite  = "\033[107m"

	ClearLine = "\033[2K"
	Up1Line   = "\033[1A"
)
