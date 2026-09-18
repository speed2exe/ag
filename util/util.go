// Package util provides small, widely-used helper functions.
package util

import (
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"encoding/json/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/term"
	"github.com/speed2exe/ag/log"
	"github.com/speed2exe/ag/tool"
)

// OptPtr returns a pointer to v if v is non-zero, otherwise nil.
func OptPtr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func StreamActions[T any](eventStream <-chan T, actions ...func(T)) {
	for event := range eventStream {
		for _, action := range actions {
			action(event)
		}
	}
}

func RenderText(termWidth int, s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return "", nil
	}
	switch s[0] {
	case '-': // for yaml
		return RenderText(termWidth, fmt.Sprintf("```yaml\n%s\n```", s))
	case '"':
		var un string
		if err := json.Unmarshal([]byte(s), &un); err != nil {
			return "", fmt.Errorf("failed to unmarshal JSON string: %w", err)
		} else {
			return RenderText(termWidth, un)
		}
	case '{':
		return RenderText(termWidth, fmt.Sprintf("```json\n%s\n```", s))
	case '<': // maybe html
		md, err := htmltomarkdown.ConvertString(s)
		if err != nil {
			return "", fmt.Errorf("failed to convert HTML to markdown: %w", err)
		} else {
			return RenderText(termWidth, md)
		}
	case '\033':
		return s, nil // already contains ANSI escape codes, assume it's already rendered
	default:
		rendered, err := newGlamourRenderer(termWidth).Render(s)
		if err != nil {
			return "", fmt.Errorf("failed to render markdown: %w", err)
		} else {
			return strings.TrimSpace(rendered), nil
		}
	}
}

func newGlamourRenderer(termWidth int) *glamour.TermRenderer {
	style := styles.DraculaStyleConfig
	for _, s := range []*ansi.StyleBlock{
		&style.Document,
		&style.BlockQuote,
		&style.Paragraph,

		&style.Heading,
		&style.H1,
		&style.H2,
		&style.H3,
		&style.H4,
		&style.H5,
		&style.H6,

		&style.Code,

		&style.Table.StyleBlock,

		&style.DefinitionList,

		&style.HTMLBlock,
		&style.HTMLSpan,
	} {
		s.Margin = nil
		s.Indent = nil
		s.IndentToken = nil
		s.StylePrimitive.BlockPrefix = ""
		s.StylePrimitive.BlockSuffix = ""
	}
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(termWidth),
	)
	return r
}

func FeedChan[T any](src <-chan T, dest chan<- T) <-chan T {
	ret := make(chan T, len(src))
	go func() {
		defer close(ret)
		defer func() {
			if recover() != nil {
				// dest was closed; just stop
			}
		}()
		for v := range src {
			dest <- v
			ret <- v
		}
	}()
	return ret
}

func ReadFileAsBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// width, height, err := util.GetTerminalSize()
func GetTerminalSize() (int, int, error) {
	w, h, err := getTerminalSizeIoCtl()
	if err != nil {
		log.Infof("failed to get terminal size via ioctl: %v, falling back to $COLUMNS and $LINES", err)
		w, h, err = terminalSizeFromEnv()
		if err != nil {
			return 0, 0, fmt.Errorf("failed to get terminal size from environment variables: %v", err)
		}
	}
	return w, h, nil
}

func getTerminalSizeIoCtl() (int, int, error) {
	fds := []uintptr{
		os.Stdin.Fd(),
		os.Stdout.Fd(),
		os.Stderr.Fd(),
	}
	for _, fd := range fds {
		// Verify if the file descriptor is actually a terminal
		if !term.IsTerminal(fd) {
			log.Infof("file descriptor %d is not a terminal, skipping", fd)
			continue
		}

		// Retrieve dimensions (width represents columns, height represents rows)
		width, height, err := term.GetSize(fd)
		if err != nil {
			log.Infof("failed to get terminal size for fd %d: %v", fd, err)
			continue
		}
		return width, height, nil
	}
	return 0, 0, fmt.Errorf("failed to get terminal size via ioctl for all standard file descriptors")
}

// terminalSizeFromEnv reads $COLUMNS and $LINES as a best-effort fallback
// when the terminal size can't be determined via ioctl.
func terminalSizeFromEnv() (width, height int, err error) {
	col, err := getEnvAsInt("COLUMNS", 0)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse $COLUMNS: %v", err)
	}
	line, err := getEnvAsInt("LINES", 0)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse $LINES: %v", err)
	}
	return col, line, nil
}

func getEnvAsInt(key string, defaultValue int) (int, error) {
	valueStr, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %s as int: %v", key, err)
	}
	return value, nil
}

func ToolSortedListFromMap(toolsByName map[string]*tool.Tool) []*tool.Tool {
	tools := make([]*tool.Tool, 0, len(toolsByName))
	for _, t := range toolsByName {
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	return tools
}
