// Package util provides small, widely-used helper functions.
package util

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/speed2exe/ag/tool"
	"golang.org/x/term"
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

func MustRenderCodeBlock(lang string, termWidth int, code string) string {
	rendered, err := RenderText(termWidth, fmt.Sprintf("```%s\n%s\n```", lang, code))
	if err != nil {
		panic(fmt.Sprintf("failed to render text: %v", err))
	}
	return trimANSISpace(rendered, true, true)
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
		}
		return RenderText(termWidth, un)
	case '{':
		return RenderText(termWidth, fmt.Sprintf("```json\n%s\n```", s))
	case '<': // maybe html
		md, err := htmltomarkdown.ConvertString(s)
		if err != nil {
			return "", fmt.Errorf("failed to convert HTML to markdown: %w", err)
		}
		return RenderText(termWidth, md)
	case '\033':
		return s, nil // already contains ANSI escape codes, assume it's already rendered
	default:
		rendered, err := newGlamourRenderer(termWidth).Render(s)
		if err != nil {
			return "", fmt.Errorf("failed to render markdown: %w", err)
		}

		// trim trailing spaces from each line to avoid extra spaces at the end of lines
		lines := strings.Split(rendered, "\n")
		for i, line := range lines {
			lines[i] = trimANSISpace(line, false, true)
		}
		return trimANSISpace(strings.Join(lines, "\n"), false, true), nil
	}
}

// trimANSISpace trims whitespace from the requested sides of s while keeping
// surrounding ANSI escape sequences intact.
func trimANSISpace(s string, left, right bool) string {
	type token struct {
		value string
		ansi  bool
	}
	var tokens []token
	first, last := -1, -1
	for state, rest := xansi.NormalState, s; len(rest) > 0; {
		seq, _, size, nextState := xansi.DecodeSequence(rest, state, nil)
		if size <= 0 {
			size = 1
			seq = rest[:1]
		}
		isANSI := len(seq) > 0 && (seq[0] == '\x1b' || seq[0] >= 0x80 && seq[0] <= 0x9f) && xansi.Strip(seq) == ""
		if !isANSI && strings.TrimSpace(seq) != "" {
			if first == -1 {
				first = len(tokens)
			}
			last = len(tokens)
		}
		tokens = append(tokens, token{value: seq, ansi: isANSI})
		state = nextState
		rest = rest[size:]
	}
	if first == -1 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, tok := range tokens {
		if tok.ansi {
			b.WriteString(tok.value)
			continue
		}
		if (left && i < first) || (right && i > last) {
			continue
		}
		v := tok.value
		if left && i == first {
			v = strings.TrimLeftFunc(v, unicode.IsSpace)
		}
		if right && i == last {
			v = strings.TrimRightFunc(v, unicode.IsSpace)
		}
		b.WriteString(v)
	}
	return b.String()
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
		&style.CodeBlock.StyleBlock,
		&style.Table.StyleBlock,
		&style.DefinitionList,
		&style.HTMLBlock,
		&style.HTMLSpan,
	} {
		s.Margin = new(uint)
		s.Indent = new(uint)
		s.IndentToken = new(string)
	}

	for _, s := range []*ansi.StylePrimitive{
		&style.Document.StylePrimitive,
		&style.BlockQuote.StylePrimitive,
		&style.Paragraph.StylePrimitive,
		&style.List.StylePrimitive,
		&style.Heading.StylePrimitive,
		&style.H1.StylePrimitive,
		&style.H2.StylePrimitive,
		&style.H3.StylePrimitive,
		&style.H4.StylePrimitive,
		&style.H5.StylePrimitive,
		&style.H6.StylePrimitive,
		&style.Text,
		&style.Strikethrough,
		&style.Emph,
		&style.Strong,
		&style.HorizontalRule,
		&style.Link,
		&style.LinkText,
		&style.Image,
		&style.ImageText,
		&style.Code.StylePrimitive,
		&style.CodeBlock.StylePrimitive,
		&style.Table.StylePrimitive,
		&style.HTMLBlock.StylePrimitive,
		&style.HTMLSpan.StylePrimitive,
	} {
		s.Prefix = ""
		s.Suffix = ""
		s.BlockPrefix = ""
		s.BlockSuffix = ""
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

func GetTerminalWidth() int {
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		fd := int(f.Fd())
		if term.IsTerminal(fd) {
			if w, _, err := term.GetSize(fd); err == nil && w > 0 {
				return w
			}
		}
	}
	if w, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && w > 0 {
		return w
	}
	return 0
}

func ToolSortedListFromMap(toolsByName map[string]*tool.Tool) []*tool.Tool {
	tools := slices.Collect(maps.Values(toolsByName))
	slices.SortFunc(tools, func(a, b *tool.Tool) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return tools
}
