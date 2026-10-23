// Package util provides small, widely-used helper functions.
package util

import (
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"encoding/json/v2"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"
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

func MustRenderText(termWidth int, s string) string {
	rendered, err := RenderText(termWidth, s)
	if err != nil {
		panic(fmt.Sprintf("failed to render text: %v", err))
	}
	return rendered
}

func MustRenderCodeBlock(lang string, termWidth int, bash string) string {
	return TrimANSISpace(MustRenderText(termWidth, fmt.Sprintf("```%s\n%s\n```", lang, bash)))
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
			lines[i] = TrimRightANSISpace(line)
		}
		return TrimRightANSISpace(strings.Join(lines, "\n")), nil
	}
}

// TrimANSISpace removes leading and trailing whitespace without removing ANSI
// escape sequences that apply to the remaining content.
func TrimANSISpace(s string) string {
	return trimANSISpace(s, true, true)
}

// TrimLeftANSISpace removes leading whitespace without removing ANSI escape
// sequences that apply to the remaining content.
func TrimLeftANSISpace(s string) string {
	return trimANSISpace(s, true, false)
}

// TrimRightANSISpace removes trailing whitespace without removing ANSI escape
// sequences that apply to the remaining content.
func TrimRightANSISpace(s string) string {
	return trimANSISpace(s, false, true)
}

// trimANSISpace trims whitespace from the requested sides of s. Whitespace is
// found by scanning runes, so escape sequences are never mistaken for text, and
// the escape sequences surrounding the trimmed whitespace are kept.
func trimANSISpace(s string, left, right bool) string {
	if !hasANSI(s) {
		switch {
		case left && right:
			return strings.TrimSpace(s)
		case left:
			return strings.TrimLeftFunc(s, unicode.IsSpace)
		default:
			return strings.TrimRightFunc(s, unicode.IsSpace)
		}
	}

	// Find the content: the run of non-space text from the first to the last
	// chunk, ignoring escape sequences. Anything trimmed off is recorded so that
	// a rewrite only happens when it is actually needed.
	var (
		first, last        = -1, 0 // content bounds in bytes
		leadText, leadANSI bool    // whitespace and escapes before the content
		tailText, tailANSI bool    // whitespace and escapes after the content
	)
	for i := 0; i < len(s); {
		// Plain ASCII is the common case and is always a chunk of its own.
		if c := s[i]; c < utf8.RuneSelf && c != '\x1b' {
			if c == ' ' || c >= '\t' && c <= '\r' {
				if first < 0 {
					leadText = true
				} else {
					tailText = true
				}
			} else {
				if first < 0 {
					first = i
				}
				last = i + 1
				tailText, tailANSI = false, false
			}
			i++
			continue
		}
		size, kind := nextChunk(s[i:])
		switch {
		case kind == chunkANSI:
			if first < 0 {
				leadANSI = true
			} else {
				tailANSI = true
			}
		case kind == chunkSpace:
			if first < 0 {
				leadText = true
			} else {
				tailText = true
			}
		default:
			if first < 0 {
				first = i
			}
			last = i + size
			tailText, tailANSI = false, false
		}
		i += size
	}
	if first < 0 {
		return "" // no content, only whitespace and escape sequences
	}

	start, end := 0, len(s)
	trimLeftANSI := left && leadText && leadANSI
	if left && leadText {
		start = first
	}
	trimRightANSI := right && tailText && tailANSI
	if right && tailText {
		end = last
	}

	switch {
	case start == 0 && end == len(s):
		return s // nothing to trim
	case !trimLeftANSI && !trimRightANSI:
		return s[start:end] // the content is contiguous
	}

	var b strings.Builder
	b.Grow(len(s))
	if trimLeftANSI {
		writeANSISequences(&b, s[:start])
	}
	b.WriteString(s[start:end])
	if trimRightANSI {
		writeANSISequences(&b, s[end:])
	}
	return b.String()
}

// ansiC1Introducers are the C1 bytes that can begin an ANSI escape sequence,
// without the ESC byte that begins the others.
const ansiC1Introducers = "\x90\x98\x9b\x9d\x9e\x9f"

// hasANSI reports whether s contains a byte that can begin an ANSI escape
// sequence. Bytes in the C1 range also appear inside multi-byte UTF-8 text, so a
// false positive only costs a slower path.
func hasANSI(s string) bool {
	// A byte loop is fastest over the prefix that decides most calls: rendered
	// text starts its first escape sequence within a few bytes.
	const inline = 16
	for i := 0; i < min(len(s), inline); i++ {
		if c := s[i]; c == '\x1b' || c >= 0x80 && c <= 0x9f {
			return true
		}
	}
	if len(s) <= inline {
		return false
	}
	// The rest of a longer string is better off in the vectorised byte search of
	// the strings package, which is several times faster than a byte loop.
	if strings.IndexByte(s, '\x1b') >= 0 {
		return true
	}
	for i := 0; i < len(ansiC1Introducers); i++ {
		if strings.IndexByte(s, ansiC1Introducers[i]) >= 0 {
			return true
		}
	}
	return false
}

// chunkKind classifies a chunk of a string that may contain ANSI escape
// sequences.
type chunkKind int

const (
	chunkText  chunkKind = iota // printable text
	chunkSpace                  // whitespace text
	chunkANSI                   // ANSI escape sequence
)

// ansiSequenceLen returns the length in bytes of the ANSI escape sequence at the
// start of s, or zero if s does not start with one. Only the bytes that
// [xansi.DecodeSequence] treats as sequence introducers count: other C1 bytes
// are passed through as opaque control bytes.
//
// A sequence that is malformed or unterminated extends to the end of s. Trimming
// bytes out from under it would change how the remaining bytes are parsed, so
// nothing after it is trimmed.
func ansiSequenceLen(s string) int {
	if !isANSIStart(s[0]) {
		return 0
	}
	if n, ok := commonSequenceLen(s); ok {
		return n
	}
	_, _, n, state := xansi.DecodeSequence(s, xansi.NormalState, nil)
	switch {
	case n <= 0:
		// A decoder that makes no progress would loop forever on invalid input.
		return 1
	case state != xansi.NormalState, introducerOnly(s[:n]), !terminatesSequence(s[:n]):
		return len(s)
	}
	return n
}

// commonSequenceLen returns the length of the well-formed escape sequence at the
// start of s and whether it could handle it. It covers the sequences that
// rendered text is made of, which [xansi.DecodeSequence] spends much more time
// on: control sequences, string sequences, and two byte escape sequences.
func commonSequenceLen(s string) (int, bool) {
	switch s[0] {
	case '\x1b':
		if len(s) < 2 {
			return 0, false
		}
		if n, ok := sequenceAfterESC(s[1:]); ok {
			return n + 1, true
		}
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f: // C1 DCS, SOS, OSC, PM, APC
		if n, ok := stringSequenceLen(s[1:]); ok {
			return n + 1, true
		}
	case 0x9b: // C1 CSI
		if n := controlSequenceLen(s[1:]); n > 0 {
			return n + 1, true
		}
	}
	return 0, false
}

// sequenceAfterESC returns the length of the well-formed escape sequence that
// follows the ESC at the start of s, which it does not include, and whether it
// could handle it.
func sequenceAfterESC(s string) (int, bool) {
	switch c := s[0]; {
	case c == '[': // CSI
		if n := controlSequenceLen(s[1:]); n > 0 {
			return n + 1, true
		}
	case c == 'P', c == ']', c == 'X', c == '^', c == '_': // DCS, OSC, SOS, PM, APC
		if n, ok := stringSequenceLen(s[1:]); ok {
			return n + 1, true
		}
	case c >= 0x30 && c <= 0x7e: // two byte escape sequence
		return 1, true
	}
	return 0, false
}

// controlSequenceLen returns the length of the control sequence at the start of
// s, which starts just after the CSI introducer, or zero if it is malformed.
func controlSequenceLen(s string) int {
	i := 0
	for ; i < len(s) && s[i] >= '<' && s[i] <= '?'; i++ { // parameter prefixes
	}
	for ; i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == ';' || s[i] == ':'); i++ { // parameters
	}
	for ; i < len(s) && s[i] >= ' ' && s[i] <= '/'; i++ { // intermediates
	}
	if i < len(s) && s[i] >= '@' && s[i] <= '~' { // final byte
		return i + 1
	}
	return 0
}

// stringSequenceLen returns the length of the terminated string sequence at the
// start of s, which starts just after its introducer.
func stringSequenceLen(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0x07, 0x9c: // BEL, ST
			return i + 1, true
		case 0x18, 0x1a: // CAN, SUB cancel the sequence
			return i, true
		case '\x1b':
			if i+1 < len(s) && s[i+1] == '\\' { // ST
				return i + 2, true
			}
			return i, true // an ESC that does not start an ST cancels the sequence
		}
	}
	return 0, false
}

// terminatesSequence reports whether b ends with a byte that ends an escape
// sequence: a final byte, or one of the control characters that terminate a
// string sequence.
func terminatesSequence(b string) bool {
	c := b[len(b)-1]
	return c >= 0x40 && c <= 0x7e || // final byte of an escape or control sequence
		c == 0x07 || c == 0x18 || c == 0x1a || c == 0x9c // BEL, CAN, SUB, ST
}

// introducerOnly reports whether b is nothing but the introducer of a control or
// string sequence: ESC, any intermediate bytes, and the letter that opens a CSI,
// DCS, OSC, SOS, PM or APC. The decoder reports such a prefix as complete when an
// invalid byte follows it, but the sequence it opens has not started yet.
func introducerOnly(b string) bool {
	if b[0] != '\x1b' {
		return false
	}
	i := 1
	for i < len(b) && b[i] >= ' ' && b[i] <= '/' { // intermediates
		i++
	}
	if i+1 != len(b) {
		return false
	}
	switch b[i] {
	case '[', 'P', ']', 'X', '^', '_': // CSI, DCS, OSC, SOS, PM, APC
		return true
	}
	return false
}

// nextChunk returns the length in bytes of the chunk at the start of s, which
// must not be empty, and what kind of chunk it is. An escape sequence is always
// taken whole and a rune is never split, so a byte that belongs to a multi-byte
// rune can never be mistaken for the start of an escape sequence.
func nextChunk(s string) (size int, kind chunkKind) {
	c := s[0]
	if c < utf8.RuneSelf && c != '\x1b' {
		// Plain ASCII is always a chunk of its own and cannot be part of an
		// escape sequence.
		if c == ' ' || c >= '\t' && c <= '\r' {
			return 1, chunkSpace
		}
		return 1, chunkText
	}
	if isANSIStart(c) {
		return ansiSequenceLen(s), chunkANSI
	}
	if r, size := utf8.DecodeRuneInString(s); r != utf8.RuneError || size > 1 {
		if unicode.IsSpace(r) {
			return size, chunkSpace
		}
		return size, chunkText
	}
	// Invalid UTF-8: a single byte, which is never whitespace. Grouping it with
	// the bytes that follow would make the parse depend on whether the whitespace
	// before it was trimmed, which would break idempotence.
	return 1, chunkText
}

// isANSIStart reports whether c can begin an ANSI escape sequence.
func isANSIStart(c byte) bool {
	switch c {
	case '\x1b', 0x90, 0x98, 0x9b, 0x9d, 0x9e, 0x9f: // ESC, DCS, SOS, CSI, OSC, PM, APC
		return true
	}
	return false
}

// writeANSISequences writes the ANSI escape sequences of s to b, dropping
// everything else.
func writeANSISequences(b *strings.Builder, s string) {
	for i := 0; i < len(s); {
		size, kind := nextChunk(s[i:])
		if kind == chunkANSI {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
}
func newGlamourRenderer(termWidth int) *glamour.TermRenderer {
	style := styles.DraculaStyleConfig

	// modify all StyleBlocks
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

	// modify all StylePrimitives
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

		// &style.Item,
		// &style.Enumeration,
		// &style.Task.StylePrimitive,

		&style.Link,
		&style.LinkText,

		&style.Image,
		&style.ImageText,

		&style.Code.StylePrimitive,
		&style.CodeBlock.StylePrimitive,

		&style.Table.StylePrimitive,

		// &style.DefinitionList.StylePrimitive,
		// &style.DefinitionTerm,
		// &style.DefinitionDescription,

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
