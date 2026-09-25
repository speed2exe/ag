//go:build !cgo

package readline

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// scanner is shared across calls so that buffered-but-unread bytes are not
// lost between consecutive ReadLine invocations. If stdin has been
// piped/redirected (e.g. `echo prompt | agent -i`), read from the
// controlling terminal instead so interactive keyboard input keeps working
// after the piped data is consumed.
var scanner = func() *bufio.Scanner {
	in := io.Reader(os.Stdin)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		if tty, err := os.Open("/dev/tty"); err == nil {
			in = tty
		}
	}
	return bufio.NewScanner(in)
}()

// ReadLine reads a line from stdin using a plain bufio.Scanner.
// This fallback is used when CGo is disabled (CGO_ENABLED=0) and GNU readline
// is therefore unavailable. Arrow-key history and line editing are not supported.
//
// Returns (line, nil) on success, or ("", io.EOF) on EOF (Ctrl+D).
func ReadLine(prompt string) (string, error) {
	fmt.Print(prompt)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	return "", io.EOF
}

func AddHistory(line string) {
	// no-op; history is not supported in the fallback implementation
}
