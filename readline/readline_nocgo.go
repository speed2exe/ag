//go:build !cgo

package readline

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

// scanner is shared across calls so that buffered-but-unread bytes are not
// lost between consecutive ReadLine invocations.
var scanner = bufio.NewScanner(os.Stdin)

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
