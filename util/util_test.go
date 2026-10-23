package util

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

// The reference implementation below is the straightforward token-per-sequence
// version of the trimming functions. It is kept as an oracle for the optimized
// implementation: the differential fuzz test below requires them to agree.

type refToken struct {
	value string
	ansi  bool
}

func refTokens(s string) []refToken {
	tokens := make([]refToken, 0, len(s))
	for state, rest := xansi.NormalState, s; len(rest) > 0; {
		sequence, _, size, nextState := xansi.DecodeSequence(rest, state, nil)
		value := string(sequence)
		ansi := len(value) > 0 && (value[0] == '\x1b' || value[0] >= 0x80 && value[0] <= 0x9f) &&
			xansi.Strip(value) == ""
		tokens = append(tokens, refToken{value: value, ansi: ansi})
		state = nextState
		rest = rest[size:]
	}
	return tokens
}

func refTrimLeftANSISpace(s string) string {
	tokens := refTokens(s)

	first := -1
	for i, token := range tokens {
		if !token.ansi && strings.TrimSpace(token.value) != "" {
			first = i
			break
		}
	}
	if first == -1 {
		return ""
	}

	var b strings.Builder
	for i, token := range tokens {
		if token.ansi {
			b.WriteString(token.value)
			continue
		}
		switch {
		case i < first:
		case i == first:
			b.WriteString(strings.TrimLeftFunc(token.value, unicode.IsSpace))
		default:
			b.WriteString(token.value)
		}
	}
	return b.String()
}

func refTrimRightANSISpace(s string) string {
	tokens := refTokens(s)

	last := -1
	for i, token := range tokens {
		if !token.ansi && strings.TrimSpace(token.value) != "" {
			last = i
		}
	}
	if last == -1 {
		return ""
	}

	var b strings.Builder
	for i, token := range tokens {
		if token.ansi {
			b.WriteString(token.value)
			continue
		}
		switch {
		case i > last:
		case i == last:
			b.WriteString(strings.TrimRightFunc(token.value, unicode.IsSpace))
		default:
			b.WriteString(token.value)
		}
	}
	return b.String()
}

func refTrimANSISpace(s string) string {
	return refTrimLeftANSISpace(refTrimRightANSISpace(s))
}

func str(s string) *string { return &s }

func TestTrimANSISpace(t *testing.T) {
	const (
		red   = "\x1b[31m"
		reset = "\x1b[0m"
		bold  = "\x1b[1m"
	)
	for _, tc := range []struct {
		name  string
		in    string
		left  *string // nil if only the two-sided trim is checked
		right *string
		both  string
	}{
		{name: "Empty", in: ""},
		{name: "Plain", in: "hello", left: str("hello"), right: str("hello"), both: "hello"},
		{name: "PlainPadded", in: "  hello  ", left: str("hello  "), right: str("  hello"), both: "hello"},
		{name: "PlainBlank", in: " \t\n "},
		{name: "ANSIPadded", in: "  " + red + "  hello  " + reset + "  ", left: str(red + "hello  " + reset + "  "), right: str("  " + red + "  hello" + reset), both: red + "hello" + reset},
		{name: "ANSTight", in: red + "hello" + reset, left: str(red + "hello" + reset), right: str(red + "hello" + reset), both: red + "hello" + reset},
		{name: "ANSITrailingInside", in: red + "hello  " + reset, left: str(red + "hello  " + reset), right: str(red + "hello" + reset), both: red + "hello" + reset},
		{name: "UnicodeSpace", in: "\u00a0\u3000hello\u2028\u2029", left: str("hello\u2028\u2029"), right: str("\u00a0\u3000hello"), both: "hello"},
		{name: "UnicodeContent", in: "  \u00e9\u65e5\u672c\u8a9e  ", left: str("\u00e9\u65e5\u672c\u8a9e  "), right: str("  \u00e9\u65e5\u672c\u8a9e"), both: "\u00e9\u65e5\u672c\u8a9e"},
		{name: "UnicodeContentANSI", in: red + " \u65e5\u672c\u8a9e " + reset, left: str(red + "\u65e5\u672c\u8a9e " + reset), right: str(red + " \u65e5\u672c\u8a9e" + reset), both: red + "\u65e5\u672c\u8a9e" + reset},
		{name: "TabAndNewline", in: "\t\nhello\r\n", left: str("hello\r\n"), right: str("\t\nhello"), both: "hello"},
		{name: "Multiline", in: "  a\n  b  \n", left: str("a\n  b  \n"), right: str("  a\n  b"), both: "a\n  b"},
		// Escape sequences may contain spaces themselves; they are kept.
		{name: "OSCTitle", in: "\x1b]0;my title\x07hello  ", left: str("\x1b]0;my title\x07hello  "), right: str("\x1b]0;my title\x07hello"), both: "\x1b]0;my title\x07hello"},
		// With no text content at all, everything is dropped, escapes included.
		{name: "ANSIOnly", in: "  " + reset + "  ", left: str(""), right: str(""), both: ""},
		{name: "ANSIPaddingBetween", in: red + reset + "   " + bold + reset + "   ", left: str(""), right: str(""), both: ""},
		{name: "OSCTitlePadded", in: "  \x1b]0;my title\x07  ", both: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.left != nil {
				if got := TrimLeftANSISpace(tc.in); got != *tc.left {
					t.Errorf("TrimLeftANSISpace(%q) = %q, want %q", tc.in, got, *tc.left)
				}
			}
			if tc.right != nil {
				if got := TrimRightANSISpace(tc.in); got != *tc.right {
					t.Errorf("TrimRightANSISpace(%q) = %q, want %q", tc.in, got, *tc.right)
				}
			}
			if got := TrimANSISpace(tc.in); got != tc.both {
				t.Errorf("TrimANSISpace(%q) = %q, want %q", tc.in, got, tc.both)
			}
		})
	}
}

// differentialApplicable reports whether the reference implementation is a sound
// oracle for s. The reference classifies decoded runs by stripping them with
// xansi.Strip, which disagrees with the optimized implementation on malformed
// input: xansi.Strip leaks raw C1 bytes and payload bytes of unterminated string
// sequences as text, and the parser groups invalid UTF-8 into runs that do not
// align with rune boundaries. Those inputs are covered by
// TestTrimANSISpaceMalformedSequences instead.
func differentialApplicable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	offset := 0
	for _, token := range refTokens(s) {
		ansi := false
		if len(token.value) > 0 && isANSIStart(token.value[0]) {
			n := ansiSequenceLen(s[offset:])
			if n != len(token.value) {
				return false // the token is only part of a sequence
			}
			ansi = n > 0
		}
		if ansi != token.ansi {
			return false
		}
		offset += len(token.value)
	}
	return true
}

// TestTrimANSISpaceDifferential checks the optimized implementation against the
// reference implementation for a hand-picked set of awkward inputs.
func TestTrimANSISpaceDifferential(t *testing.T) {
	inputs := []string{
		"", " ", "\t", "\x1b", "\x1b[", "\x1b[31", "\x1b[31m", "\x1b]0;hi", "\x1b]0;hi\x07",
		"\x1b]0;hi\x1b\\", " \x80 ", "\x85", "\x9b31m", "\x9b", "\xff",
		"  \x9b31m  hi  \x9d  ", "a\x00b", " \x00 ", "\u00a0", "\x1b[31m\u00a0\x1b[0m",
		"  \x1b[31m  \x1b[0m  ", "\r\n\t \x1b[1mhi\x1b[22m \r\n\t",
		"\x1bP", "\x1bP;", "\x1bX", "\x1b_hi ", "\x1b[3 hi ", "\xc2\x9b hi ", "\x90]",
		"e\u0301 ", "\u3000\x1b[1m\u65e5\u672c\u8a9e\u2028", "\x1b[0m\x1b[0m \x1b[0m",
	}
	for _, in := range inputs {
		checkAgainstReference(t, in)
	}
}

// TestTrimANSISpaceMalformedSequences pins down the behavior for malformed
// input, where the optimized implementation deliberately differs from the
// reference: an unterminated string sequence is treated as an escape sequence
// rather than as text to be kept.
func TestTrimANSISpaceMalformedSequences(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x9e\x800", ""},          // unterminated SOS; reference keeps the payload as text
		{"\x1b X0 ", ""},           // ESC + intermediate + SOS; reference keeps it as text
		{"\x80  hi  ", "\x80  hi"}, // a lone C1 byte is not an escape sequence
		{"\x85 ", "\x85"},
		// A malformed escape sequence is taken to extend to the end of the
		// string, so the whitespace after it is dropped along with the rest
		// instead of being trimmed. The reference trims it and leaves the broken
		// sequence in place.
		{"\x1b\x01 ", ""},
		{"\x1b\v0", ""},
		{"\x1b[3\t1m ", ""},
	} {
		if got := TrimANSISpace(tc.in); got != tc.want {
			t.Errorf("TrimANSISpace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHasANSI checks the detection that picks the plain text path, including the
// vectorised search that takes over past the byte-at-a-time prefix.
func TestHasANSI(t *testing.T) {
	const inline = 16 // the length of the byte-at-a-time prefix of hasANSI
	pad := strings.Repeat("a", 2*inline)
	for b := 0; b < 256; b++ {
		c := byte(b)
		for _, pos := range []int{0, inline - 1, inline, inline + 1, len(pad) - 1} {
			s := make([]byte, 0, len(pad)+1)
			s = append(append(append(s, pad[:pos]...), c), pad[pos:]...)
			// The prefix also flags the rest of the C1 range, which costs a
			// slower path but is never wrong.
			want := isANSIStart(c)
			if pos < inline {
				want = want || c >= 0x80 && c <= 0x9f
			}
			if got := hasANSI(string(s)); got != want {
				t.Fatalf("hasANSI(pad[:%d] + %#x + pad[%d:]) = %t, want %t", pos, c, pos, got, want)
			}
		}
	}
}

func FuzzTrimANSISpace(f *testing.F) {
	seeds := []string{
		"", "  hello  ", "  \x1b[31m  hello  \x1b[0m  ", "\x1b]0;title with spaces\x07  x  ",
		"\x1b[38;5;81mfunc\x1b[0m\x1b[38;5;231m \x1b[0m\x1b[38;5;192mmain\x1b[0m",
		"\u00a0\u3000\x1b[1m\u65e5\u672c\u8a9e\u2028", "\x1b", "\x1b[", "\x90", "\x9b", "\xff\xfe",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		both := TrimANSISpace(s)

		// Trimming only ever removes bytes; it never invents any.
		if !isSubsequence(both, s) {
			t.Fatalf("TrimANSISpace(%q) = %q is not a subsequence of its input", s, both)
		}
		if differentialApplicable(s) {
			checkAgainstReference(t, s)
		}
		if !utf8.ValidString(s) {
			// Invalid UTF-8 can be grouped into different runes once bytes are
			// trimmed away, so the invariants below do not hold for it.
			return
		}

		// The trimmed result must be stable and must not have blank edges.
		if again := TrimANSISpace(both); again != both {
			t.Fatalf("TrimANSISpace is not idempotent: %q -> %q -> %q", s, both, again)
		}
		if hasBlankEdge(both) {
			t.Fatalf("TrimANSISpace(%q) = %q leaves whitespace edges", s, both)
		}
		// Trimming one side at a time must agree with trimming both.
		if got := TrimLeftANSISpace(TrimRightANSISpace(s)); got != both {
			t.Fatalf("TrimANSISpace(%q) = %q, TrimLeftANSISpace(TrimRightANSISpace(s)) = %q", s, both, got)
		}
		if got := TrimRightANSISpace(TrimLeftANSISpace(s)); got != both {
			t.Fatalf("TrimANSISpace(%q) = %q, TrimRightANSISpace(TrimLeftANSISpace(s)) = %q", s, both, got)
		}
		// Plain text is trimmed like strings.TrimSpace.
		if !hasANSI(s) && both != strings.TrimSpace(s) {
			t.Fatalf("TrimANSISpace(%q) = %q, want %q", s, both, strings.TrimSpace(s))
		}
		// Escape sequences must survive the trim, unless there is no text
		// content left at all.
		if got, want := countANSISequences(both), countANSISequences(s); both != "" && got < want {
			t.Fatalf("TrimANSISpace(%q) = %q dropped escape sequences: %d < %d", s, both, got, want)
		}
	})
}

// isSubsequence reports whether sub is a subsequence of s, byte by byte.
func isSubsequence(sub, s string) bool {
	i := 0
	for j := 0; j < len(s) && i < len(sub); j++ {
		if sub[i] == s[j] {
			i++
		}
	}
	return i == len(sub)
}

func checkAgainstReference(t *testing.T, s string) {
	t.Helper()
	if got, want := TrimLeftANSISpace(s), refTrimLeftANSISpace(s); got != want {
		t.Fatalf("TrimLeftANSISpace(%q) = %q, reference = %q", s, got, want)
	}
	if got, want := TrimRightANSISpace(s), refTrimRightANSISpace(s); got != want {
		t.Fatalf("TrimRightANSISpace(%q) = %q, reference = %q", s, got, want)
	}
	if got, want := TrimANSISpace(s), refTrimANSISpace(s); got != want {
		t.Fatalf("TrimANSISpace(%q) = %q, reference = %q", s, got, want)
	}
}

// hasBlankEdge reports whether s has whitespace text before its first or after
// its last non-space rune, ignoring escape sequences.
func hasBlankEdge(s string) bool {
	first := -1
	var leadText, tailText bool
	for i := 0; i < len(s); {
		size, kind := nextChunk(s[i:])
		switch {
		case kind == chunkANSI:
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
			tailText = false
		}
		i += size
	}
	return leadText || tailText
}

func countANSISequences(s string) int {
	var n int
	for i := 0; i < len(s); {
		if size := ansiSequenceLen(s[i:]); size > 0 {
			n++
			i += size
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return n
}

// The inputs below mirror what the renderer hands to the trimming functions: a
// line of rendered markdown, a code line, a padding-only line, and plain text.
var benchInputs = []struct {
	name string
	in   string
}{
	{"RenderedLine", "  \x1b[38;5;228m  This is a paragraph of rendered markdown  \x1b[0m  "},
	{"CodeLine", "\x1b[38;5;81mfunc\x1b[0m\x1b[38;5;231m \x1b[0m\x1b[38;5;192mmain\x1b[0m\x1b[38;5;231m()\x1b[0m \x1b[38;5;231m{\x1b[0m  "},
	{"PaddingOnly", "\x1b[38;5;81m\x1b[0m\x1b[38;5;231m   \x1b[0m  "},
	{"OSCTitle", "\x1b]0;a title with spaces\x07  hello  "},
	{"Hyperlink", "  \x1b]8;;https://example.com/a/very/long/path\x1b\\a link\x1b]8;;\x1b\\  "},
	{"Unicode", "  \u65e5\u672c\u8a9e\u306e\u30c6\u30ad\u30b9\u30c8  "},
	{"PlainShort", "  hello  "},
	{"PlainLong", "  " + strings.Repeat("lorem ipsum dolor sit amet, consectetur adipiscing elit ", 6) + "  "},
	{"AlreadyTrimmed", "\x1b[1mbold\x1b[22m text"},
}

func BenchmarkTrimANSISpace(b *testing.B) {
	for _, input := range benchInputs {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = TrimANSISpace(input.in)
			}
		})
	}
}

func BenchmarkTrimRightANSISpace(b *testing.B) {
	for _, input := range benchInputs {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = TrimRightANSISpace(input.in)
			}
		})
	}
}

// BenchmarkReference benchmarks the token-per-sequence implementation that the
// trimming functions started out as, for comparison.
func BenchmarkReference(b *testing.B) {
	for _, input := range benchInputs {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = refTrimRightANSISpace(input.in)
			}
		})
	}
}
