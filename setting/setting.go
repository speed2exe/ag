package setting

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/speed2exe/ag/log"
	"golang.org/x/term"
)

var (
	longByShort = func() map[string]string {
		m := map[string]string{}
		for _, def := range allSettingDefs {
			short := def.shortName()
			if short != nil {
				if _, exists := m[*short]; exists {
					panic(fmt.Sprintf("duplicate short option: -%s", *short)) // for debugging
				}
				m[*short] = def.longName()
			}
		}
		return m
	}()

	knownLongNames = func() map[string]struct{} {
		m := map[string]struct{}{}
		for _, def := range allSettingDefs {
			m[def.longName()] = struct{}{}
		}
		m["help"] = struct{}{}
		return m
	}()

	shortS = "s"
	shortC = "c"
	shortI = "i"

	homeDir = func() string {
		h, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("error getting user home directory: %v", err)
			return ""
		}
		return h
	}()
	defaultLogPath                     = "./ag.log"
	defaultInstructionPath             = homeDir + "/.ag/instruction.md"
	defaultSessionPath                 = homeDir + "/.ag/sessions.json"
	defaultContextDir                  = homeDir + "/.ag/contexts"
	defaultContextTtl                  = 7 * 24 * time.Hour // 7 days
	defaultHistoryFilePath             = homeDir + "/.ag/history.json"
	defaultMaxHistoryCount             = 128
	defaultMaxMessagesBeforeUserPrompt = 32
	defaultMaxMessagesAfterUserPrompt  = 64
)

type Settings struct {
	LogFilePath                 string
	LoggingEnabled              bool
	SkillPaths                  []string
	PreloadSkillPaths           []string
	McpConfPaths                []string
	InstructionPath             string
	SessionPath                 string
	SessionKey                  string
	ContextDir                  string
	ContextId                   string
	ContextTtl                  time.Duration
	Interactive                 bool
	HistoryFilePath             string
	HistoryMaxCount             int
	MaxMessagesBeforeUserPrompt int
	MaxMessagesAfterUserPrompt  int

	UserInput strings.Builder

	LlmProvider LllmProvider

	AnthropicBaseUrl string
	AnthropicModel   string

	OpenAIBaseURL string
	OpenAIApiKey  string
	OpenAIModel   string
}

func Init() (*Settings, error) {
	settings, err := initSettings()
	if err != nil {
		return nil, fmt.Errorf("error initializing settings: %w", err)
	}
	return settings, nil
}

func initSettings() (*Settings, error) {
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" {
			printHelp()
			os.Exit(0)
		}
	}

	cliArgs, err := parseCliArgs(os.Args[1:])
	if err != nil {
		return nil, fmt.Errorf("error parsing command-line arguments: %w", err)
	}

	p := Settings{}
	for _, f := range allSettingDefs {
		haveApplied := f.applyDefault(&p)

		if applied, err := f.applyEnv(&p); err != nil {
			return nil, err
		} else {
			haveApplied = haveApplied || applied
		}
		if applied, err := f.applyArgs(&p, cliArgs); err != nil {
			return nil, err
		} else {
			haveApplied = haveApplied || applied
		}

		if !haveApplied {
			var envHelpText string
			if envName := f.envName(); envName != nil {
				envHelpText = fmt.Sprintf(" or environment variable %s", *envName)
			}
			return nil, fmt.Errorf("missing required setting: --%s%s", f.longName(), envHelpText)
		}
	}

	// Read user input from stdin if not in interactive mode. In interactive
	// mode, still read stdin when it has been piped/redirected (e.g. `echo
	// hi | agent -i`) so the piped text becomes the first turn; but skip it
	// when stdin is a real terminal, since blocking on ReadAll would wait for
	// EOF before the REPL even starts.
	if !p.Interactive || !term.IsTerminal(int(os.Stdin.Fd())) {
		inputBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("error reading user input from stdin: %w", err)
		}
		p.UserInput.Write(inputBytes)
	}

	if p.SessionKey != "" && p.ContextId != "" {
		return nil, fmt.Errorf("cannot specify both --session-key (AG_SESSION_KEY) and --context-id (AG_CONTEXT_ID); use one or the other")
	}

	return &p, nil
}

func parseCliArgs(argv []string) (map[string][]*string, error) {
	args := map[string][]*string{}
	if err := parseCliArgsAcc(args, argv); err != nil {
		return nil, err
	}
	return args, nil
}

func parseCliArgsAcc(acc map[string][]*string, argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	first := argv[0]
	if len(first) == 0 || first[0] != '-' {
		return fmt.Errorf("unexpected positional argument: %s", first)
	}
	if len(first) == 1 {
		return fmt.Errorf("invalid option format: %s (use -x or --xyz)", first)
	}

	key := first[1:]
	var isLong bool
	if key[0] == '-' {
		isLong = true
		key = key[1:]
	}

	if !isLong {
		longName, ok := longByShort[key]
		if !ok {
			return fmt.Errorf("unknown short option: -%s", key)
		}
		key = longName
	} else {
		if _, ok := knownLongNames[key]; !ok {
			return fmt.Errorf("unknown option: --%s", key)
		}
	}

	var val *string
	consumed := 1
	if len(argv) > 1 && (len(argv[1]) == 0 || argv[1][0] != '-') {
		val = &argv[1]
		consumed = 2
	}
	acc[key] = append(acc[key], val)

	return parseCliArgsAcc(acc, argv[consumed:])
}

type settingDef interface {
	longName() string
	shortName() *string
	envName() *string
	description() string
	applyDefault(*Settings) bool
	applyEnv(*Settings) (bool, error)
	applyArgs(*Settings, map[string][]*string) (bool, error)
	usageFlag() usageFlag
}

type usageFlag struct {
	longName        string
	shortName       *string
	expectsValue    bool
	description     string
	defaultValue    any
	hasDefaultValue bool
	required        bool
	example         string
}

type flagDefImpl[T any] struct {
	defVal *T // keep it nil if you require that this field must be set by the user
	name   string
	short  *string
	env    *string
	desc   string

	apply      func(*Settings, T) error
	fromStr    func(string) (T, error)
	fromStrArr func([]*string) (T, error)
}

func (f *flagDefImpl[T]) longName() string {
	return f.name
}

func (f *flagDefImpl[T]) shortName() *string {
	return f.short
}

func (f *flagDefImpl[T]) envName() *string {
	return f.env
}

func (f *flagDefImpl[T]) description() string {
	return f.desc
}

func (f *flagDefImpl[T]) applyDefault(s *Settings) bool {
	if f.defVal != nil {
		if err := f.apply(s, *f.defVal); err != nil {
			panic(fmt.Sprintf("error applying default value for --%s: %v", f.name, err)) // should never happen, panic for debugging
		}
		return true
	}
	return false
}

func (f *flagDefImpl[T]) applyEnv(s *Settings) (bool, error) {
	if f.env == nil {
		return false, nil
	}
	value, exists := os.LookupEnv(*f.env)
	if !exists {
		return false, nil
	}
	parsed, err := f.fromStr(value)
	if err != nil {
		return false, fmt.Errorf("error parsing environment variable %s: %w", *f.env, err)
	}
	if err := f.apply(s, parsed); err != nil {
		return false, fmt.Errorf("error applying environment variable %s: %w", *f.env, err)
	}
	return true, nil
}

func (f *flagDefImpl[T]) applyArgs(s *Settings, args map[string][]*string) (bool, error) {
	values, exists := args[f.name]
	if !exists {
		return false, nil
	}
	parsed, err := f.fromStrArr(values)
	if err != nil {
		return false, fmt.Errorf("error parsing command-line argument --%s: %w", f.name, err)
	}
	f.apply(s, parsed)
	return true, nil
}

var allSettingDefs = []settingDef{
	&flagDefImpl[string]{
		name:       "instruction-path",
		env:        new("AG_INSTRUCTION_PATH"),
		defVal:     new(defaultInstructionPath),
		desc:       "Path to a Markdown file whose contents are prepended to the system prompt",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.InstructionPath = v; return nil },
	},
	&flagDefImpl[string]{
		name:       "log-path",
		env:        new("AG_LOG_PATH"),
		defVal:     new(defaultLogPath),
		desc:       "Log file path",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.LogFilePath = v; return nil },
	},
	&flagDefImpl[bool]{
		name:       "logging-enabled",
		env:        new("AG_LOGGING_ENABLED"),
		desc:       "Enable logging to file. By default, logging is disabled. If enabled, logs will be written to the path specified by --log-path.",
		fromStr:    strconv.ParseBool,
		fromStrArr: boolFromStrLastArg,
		apply:      func(s *Settings, v bool) error { s.LoggingEnabled = v; return nil },
		defVal:     new(false),
	},
	&flagDefImpl[string]{
		name:       "session-path",
		env:        new("AG_SESSION_PATH"),
		desc:       "Path to a JSON file that stores mapping of session keys to context IDs",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.SessionPath = v; return nil },
		defVal:     new(defaultSessionPath),
	},
	&flagDefImpl[string]{
		name:       "session-key",
		env:        new("AG_SESSION_KEY"),
		short:      &shortS,
		desc:       "Specify a session key to use for this run or resume a previous session, session key is mapped to a context ID and would be updated on conversation turn.",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.SessionKey = v; return nil },
		defVal:     new(""),
	},
	&flagDefImpl[string]{
		name:       "context-dir",
		env:        new("AG_CONTEXT_DIR"),
		desc:       "Directory to store conversation contexts.",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.ContextDir = v; return nil },
		defVal:     new(defaultContextDir),
	},
	&flagDefImpl[string]{
		name:       "context-id",
		env:        new("AG_CONTEXT_ID"),
		short:      &shortC,
		desc:       "Context ID from a previous run, to continue a prior conversation",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.ContextId = v; return nil },
		defVal:     new(""),
	},
	&flagDefImpl[time.Duration]{
		name:       "context-ttl",
		env:        new("AG_CONTEXT_TTL"),
		desc:       "Time-to-live for conversation contexts. Contexts older than this duration will be automatically deleted.",
		fromStr:    time.ParseDuration,
		fromStrArr: func(arr []*string) (time.Duration, error) { return time.ParseDuration(*arr[len(arr)-1]) },
		apply:      func(s *Settings, v time.Duration) error { s.ContextTtl = v; return nil },
		defVal:     new(defaultContextTtl),
	},
	&flagDefImpl[[]string]{
		name:       "skill-paths",
		env:        new("AG_SKILL_PATHS"),
		desc:       "comma-separated list of paths to search for skill definitions. Paths can be directories or SKILL.md files. Directories are searched recursively for SKILL.md files.",
		apply:      func(s *Settings, v []string) error { s.SkillPaths = append(s.SkillPaths, v...); return nil },
		fromStr:    strArrFromStr,
		fromStrArr: strArrFromStrArr,
		defVal:     &[]string{},
	},
	&flagDefImpl[[]string]{
		name: "preload-skill-paths",
		env:  new("AG_PRELOAD_SKILL_PATHS"),
		desc: "like skill-paths but loaded during agent initialization instead of on demand. Use this for skills that must be available from the start.",
		apply: func(s *Settings, v []string) error {
			s.PreloadSkillPaths = append(s.PreloadSkillPaths, v...)
			return nil
		},
		fromStr:    strArrFromStr,
		fromStrArr: strArrFromStrArr,
		defVal:     &[]string{},
	},
	&flagDefImpl[[]string]{
		name:       "mcp-conf-paths",
		env:        new("AG_MCP_CONF_PATHS"),
		desc:       "comma-separated list of paths to MCP config files. See: https://gofastmcp.com/integrations/mcp-json-configuration",
		fromStr:    strArrFromStr,
		fromStrArr: strArrFromStrArr,
		apply:      func(s *Settings, v []string) error { s.McpConfPaths = append(s.McpConfPaths, v...); return nil },
		defVal:     &[]string{},
	},
	&flagDefImpl[bool]{
		name:       "interactive",
		short:      &shortI,
		desc:       "Run in interactive mode, reading input from the terminal turn by turn.",
		fromStr:    strconv.ParseBool,
		fromStrArr: boolFromStrLastArg,
		apply:      func(s *Settings, v bool) error { s.Interactive = v; return nil },
		defVal:     new(false),
	},
	&flagDefImpl[string]{
		name:       "history-file-path",
		env:        new("AG_HISTORY_FILE_PATH"),
		desc:       "Directory to store historical user input",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.HistoryFilePath = v; return nil },
		defVal:     new(defaultHistoryFilePath),
	},
	&flagDefImpl[int]{
		name:       "history-max-count",
		env:        new("AG_HISTORY_MAX_COUNT"),
		desc:       "Maximum number of historical user input to keep",
		fromStr:    strconv.Atoi,
		fromStrArr: func(arr []*string) (int, error) { return strconv.Atoi(*arr[len(arr)-1]) },
		apply:      func(s *Settings, v int) error { s.HistoryMaxCount = v; return nil },
		defVal:     new(defaultMaxHistoryCount),
	},
	&flagDefImpl[int]{
		name:       "max-messages-before-user-prompt",
		env:        new("AG_MAX_MESSAGES_BEFORE_USER_PROMPT"),
		desc:       "Maximum number of messages to keep before the last user prompt, older messages will be summarized and replaced with a summary message",
		fromStr:    strconv.Atoi,
		fromStrArr: func(arr []*string) (int, error) { return strconv.Atoi(*arr[len(arr)-1]) },
		apply:      func(s *Settings, v int) error { s.MaxMessagesBeforeUserPrompt = v; return nil },
		defVal:     new(defaultMaxMessagesBeforeUserPrompt),
	},
	&flagDefImpl[int]{
		name:       "max-messages-after-user-prompt",
		env:        new("AG_MAX_MESSAGES_AFTER_USER_PROMPT"),
		desc:       "Maximum number of messages to keep after the last user prompt, older messages will be summarized and replaced with a summary message",
		fromStr:    strconv.Atoi,
		fromStrArr: func(arr []*string) (int, error) { return strconv.Atoi(*arr[len(arr)-1]) },
		apply:      func(s *Settings, v int) error { s.MaxMessagesAfterUserPrompt = v; return nil },
		defVal:     new(defaultMaxMessagesAfterUserPrompt),
	},

	&flagDefImpl[string]{
		name:       "llm-provider",
		env:        new("AG_LLM_PROVIDER"),
		desc:       "LLM provider to use, e.g. 'anthropic', or 'openai'",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { return setLLmProvider(s, v) },
	},

	&flagDefImpl[string]{
		name:       "anthropic-base-url",
		env:        new("AG_ANTHROPIC_BASE_URL"),
		desc:       "Anthropic API base URL",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.AnthropicBaseUrl = v; return nil },
		defVal:     new(string),
	},
	&flagDefImpl[string]{
		name:       "anthropic-model",
		env:        new("AG_ANTHROPIC_MODEL"),
		desc:       "Anthropic model name, e.g. glm-5",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.AnthropicModel = v; return nil },
		defVal:     new(string),
	},

	&flagDefImpl[string]{
		name:       "openai-base-url",
		env:        new("AG_OPENAI_BASE_URL"),
		desc:       "OpenAI-compatible API base URL",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.OpenAIBaseURL = v; return nil },
		defVal:     new(string),
	},
	&flagDefImpl[string]{
		name:       "openai-api-key",
		env:        new("AG_OPENAI_API_KEY"),
		desc:       "OpenAI API key",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.OpenAIApiKey = v; return nil },
		defVal:     new(string),
	},
	&flagDefImpl[string]{
		name:       "openai-model",
		env:        new("AG_OPENAI_MODEL"),
		desc:       "OpenAI model name, e.g. gpt-4o",
		fromStr:    strFromStr,
		fromStrArr: strFromLastArg,
		apply:      func(s *Settings, v string) error { s.OpenAIModel = v; return nil },
		defVal:     new(string),
	},
}

func strFromStr(s string) (string, error) {
	return s, nil
}

func strFromLastArg(arr []*string) (string, error) {
	if len(arr) == 0 {
		return "", errors.New("expected at least one value but got none")
	}
	val := arr[len(arr)-1]
	if val == nil {
		return "", errors.New("expected value for flag but got none")
	}
	return *val, nil
}

func boolFromStrLastArg(arr []*string) (bool, error) {
	if len(arr) == 0 {
		return false, errors.New("expected at least one value but got none")
	}
	val := arr[len(arr)-1]
	if val == nil {
		return true, nil // flag present without a value → true
	}
	return strconv.ParseBool(*val)
}

func strArrFromStr(s string) ([]string, error) {
	var result []string
	for p := range strings.SplitSeq(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result, nil
}

func strArrFromStrArr(arr []*string) ([]string, error) {
	var result []string
	for _, s := range arr {
		if s == nil {
			continue
		}
		parts, err := strArrFromStr(*s)
		if err != nil {
			return nil, err
		}
		result = append(result, parts...)
	}
	return result, nil
}

func (f *flagDefImpl[T]) usageFlag() usageFlag {
	var zero T
	_, isBool := any(zero).(bool)

	return usageFlag{
		longName:     f.name,
		shortName:    f.short,
		expectsValue: !isBool,
		description:  f.desc,
		required:     f.defVal == nil && f.env == nil,
	}

}

func printHelp() {
	shortH := "h"
	flags := []usageFlag{
		{
			longName:     "help",
			shortName:    &shortH,
			expectsValue: false,
			description:  "Show this help message.",
		},
	}
	for _, def := range allSettingDefs {
		flags = append(flags, def.usageFlag())
	}
	writeUsage(os.Stdout, os.Args[0], flags)
}

func writeUsage(output io.Writer, programName string, flags []usageFlag) {
	fmt.Fprintf(output, "Usage: %s [options]\n\n", programName)
	fmt.Fprintln(output, "Input:")
	fmt.Fprintln(output, "  Read the user message from stdin.")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Options:")
	for _, flag := range flags {
		printFlagHelp(output, flag)
	}
}

func printFlagHelp(output io.Writer, flag usageFlag) {
	fmt.Fprintf(output, "  %s\n", flagLabel(flag))
	fmt.Fprintf(output, "      %s\n", flag.description)
	if flag.hasDefaultValue {
		fmt.Fprintf(output, "      default: %s\n", formatDefaultValue(flag.defaultValue))
	} else if flag.required {
		fmt.Fprintln(output, "      required: yes")
	}
	if flag.example != "" {
		fmt.Fprintf(output, "      example: %s\n", flag.example)
	}
	fmt.Fprintln(output)
}

func flagLabel(flag usageFlag) string {
	label := longOption(flag.longName)
	if hasShortName(flag.shortName) {
		label = fmt.Sprintf("%s, %s", shortOption(*flag.shortName), label)
	}
	if flag.expectsValue {
		label += " <" + flag.longName + ">"
	}
	return label
}

func formatDefaultValue(value any) string {
	switch v := value.(type) {
	case string:
		if v == "" {
			return "(empty)"
		}
		return v
	case []string:
		if len(v) == 0 {
			return "(empty)"
		}
		return strings.Join(v, ", ")
	default:
		return fmt.Sprint(v)
	}
}

func hasShortName(name *string) bool {
	return name != nil && strings.TrimSpace(*name) != ""
}

// ShortOption formats a short flag name (e.g. "v") as "-v".
func shortOption(name string) string {
	return "-" + name
}

// LongOption formats a long flag name (e.g. "verbose") as "--verbose".
func longOption(name string) string {
	return "--" + name
}

type LllmProvider string

const (
	LlmProviderAnthropic LllmProvider = "anthropic"
	LlmProviderOpenAI    LllmProvider = "openai"
)

func setLLmProvider(p *Settings, provider string) error {
	switch provider {
	case string(LlmProviderAnthropic):
		p.LlmProvider = LlmProviderAnthropic
	case string(LlmProviderOpenAI):
		p.LlmProvider = LlmProviderOpenAI
	default:
		return fmt.Errorf("unsupported llm provider: %s", provider)
	}
	return nil
}
