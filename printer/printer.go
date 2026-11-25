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
		ticker := time.NewTicker(177 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.Flush()
			}
		}
	}()
	return p
}

func (p *Printer) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.indent == 0 {
		return p.output.Write(data)
	}
	indent := bytes.Repeat([]byte("  "), p.indent)
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
		p.output.Write(indent)
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

// Print writes strs atomically to the configured writer with no added color or newline.
func (p *Printer) Print(strs ...string) {
	p.Write([]byte(strings.Join(strs, "")))
}

const (
	Reset = "\033[0m"

	ColorRed         = "\033[31m"
	ColorGreen       = "\033[32m"
	ColorYellow      = "\033[33m"
	ColorBlue        = "\033[34m"
	ColorPurple      = "\033[35m"
	ColorCyan        = "\033[36m"
	ColorBrightBlack = "\033[90m"

	BgRed         = "\033[41m"
	BgYellow      = "\033[43m"
	BgWhite       = "\033[47m"
	BgBrightBlack = "\033[100m"

	ClearLine = "\033[2K"
	Up1Line   = "\033[1A"
)
