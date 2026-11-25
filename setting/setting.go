package setting

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/speed2exe/ag/log"
	"golang.org/x/term"
)

var (
	StdinIsTerm = term.IsTerminal(int(os.Stdin.Fd()))

	longByShort = func() map[string]string {
		m := map[string]string{}
		for _, def := range allSettingDefs {
			if short := def.shortName(); short != "" {
				if _, exists := m[short]; exists {
					panic(fmt.Sprintf("duplicate short option: -%s", short))
				}
				m[short] = def.longName()
			}
		}
		return m
	}()

	knownLongNames = func() map[string]struct{} {
		m := map[string]struct{}{"help": {}}
		for _, def := range allSettingDefs {
			m[def.longName()] = struct{}{}
		}
		return m
	}()

	homeDir = func() string {
		h, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("error getting user home directory: %v", err)
			return ""
		}
		return h
	}()
	defaultLogPath                     = "./ag.log"
	defaultInstructionPath             = filepath.Join(homeDir, ".ag", "instruction.md")
	defaultSessionPath                 = filepath.Join(homeDir, ".ag", "sessions.json")
	defaultContextDir                  = filepath.Join(homeDir, ".ag", "contexts")
	defaultContextTtl                  = 7 * 24 * time.Hour // 7 days
	defaultHistoryFilePath             = filepath.Join(homeDir, ".ag", "history.json")
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
	A2aUrls                     []string
	InstructionPath             string
	SessionPath                 string
	SessionKey                  string
	ContextDir                  string
	ContextId                   string
	ContextTtl                  time.Duration
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

	GeminiBaseURL string
	GeminiApiKey  string
	GeminiModel   string
}

func Init() (*Settings, error) {
	if slices.ContainsFunc(os.Args[1:], func(arg string) bool { return arg == "-h" || arg == "--help" }) {
		printHelp()
		os.Exit(0)
	}

	cliArgs, err := parseCliArgs(os.Args[1:])
	if err != nil {
		return nil, fmt.Errorf("error initializing settings: error parsing command-line arguments: %w", err)
	}

	p := Settings{}
	for _, f := range allSettingDefs {
		haveApplied := f.applyDefault(&p)

		applied, err := f.applyEnv(&p)
		if err != nil {
			return nil, fmt.Errorf("error initializing settings: %w", err)
		}
		haveApplied = haveApplied || applied

		applied, err = f.applyArgs(&p, cliArgs)
		if err != nil {
			return nil, fmt.Errorf("error initializing settings: %w", err)
		}
		haveApplied = haveApplied || applied

		if !haveApplied {
			var envHelpText string
			if envName := f.envName(); envName != "" {
				envHelpText = fmt.Sprintf(" or environment variable %s", envName)
			}
			return nil, fmt.Errorf("error initializing settings: missing required setting: --%s%s", f.longName(), envHelpText)
		}
	}

	// If stdin is piped, read user input into UserInput.
	if !StdinIsTerm {
		if _, err := io.Copy(&p.UserInput, os.Stdin); err != nil {
			return nil, fmt.Errorf("error initializing settings: error reading user input from stdin: %w", err)
		}
	}

	if p.SessionKey != "" && p.ContextId != "" {
		return nil, fmt.Errorf("error initializing settings: cannot specify both --session-key (AG_SESSION_KEY) and --context-id (AG_CONTEXT_ID); use one or the other")
	}

	return &p, nil
}

func parseCliArgs(argv []string) (map[string][]*string, error) {
	args := map[string][]*string{}
	for i := 0; i < len(argv); {
		first := argv[i]
		if len(first) == 0 || first[0] != '-' {
			return nil, fmt.Errorf("unexpected positional argument: %s", first)
		}
		if len(first) == 1 {
			return nil, fmt.Errorf("invalid option format: %s (use -x or --xyz)", first)
		}

		key, isLong := strings.CutPrefix(first[1:], "-")
		if !isLong {
			longName, ok := longByShort[key]
			if !ok {
				return nil, fmt.Errorf("unknown short option: -%s", key)
			}
			key = longName
		} else if _, ok := knownLongNames[key]; !ok {
			return nil, fmt.Errorf("unknown option: --%s", key)
		}

		var val *string
		i++
		if i < len(argv) && (len(argv[i]) == 0 || argv[i][0] != '-') {
			val = &argv[i]
			i++
		}
		args[key] = append(args[key], val)
	}
	return args, nil
}

type settingDef interface {
	longName() string
	shortName() string
	envName() string
	applyDefault(*Settings) bool
	applyEnv(*Settings) (bool, error)
	applyArgs(*Settings, map[string][]*string) (bool, error)
	usageFlag() usageFlag
}

type usageFlag struct {
	longName        string
	shortName       string
	envName         string
	expectsValue    bool
	description     string
	defaultValue    string
	hasDefaultValue bool
	required        bool
}

type flagDefImpl[T any] struct {
	defVal *T // keep it nil if this field must be set by the user
	name   string
	short  string
	env    string
	desc   string

	apply      func(*Settings, T) error
	fromStr    func(string) (T, error)
	fromStrArr func([]*string) (T, error) // nil defaults to strFromLastArg + fromStr
}

func (f *flagDefImpl[T]) longName() string  { return f.name }
func (f *flagDefImpl[T]) shortName() string { return f.short }
func (f *flagDefImpl[T]) envName() string   { return f.env }

func (f *flagDefImpl[T]) applyDefault(s *Settings) bool {
	if f.defVal == nil {
		return false
	}
	if err := f.apply(s, *f.defVal); err != nil {
		panic(fmt.Sprintf("error applying default value for --%s: %v", f.name, err))
	}
	return true
}

func (f *flagDefImpl[T]) applyEnv(s *Settings) (bool, error) {
	if f.env == "" {
		return false, nil
	}
	value, exists := os.LookupEnv(f.env)
	if !exists {
		return false, nil
	}
	parsed, err := f.fromStr(value)
	if err != nil {
		return false, fmt.Errorf("error parsing environment variable %s: %w", f.env, err)
	}
	if err := f.apply(s, parsed); err != nil {
		return false, fmt.Errorf("error applying environment variable %s: %w", f.env, err)
	}
	return true, nil
}

func (f *flagDefImpl[T]) applyArgs(s *Settings, args map[string][]*string) (bool, error) {
	values, exists := args[f.name]
	if !exists {
		return false, nil
	}
	var (
		parsed T
		err    error
	)
	if f.fromStrArr != nil {
		parsed, err = f.fromStrArr(values)
	} else {
		var raw string
		if raw, err = strFromLastArg(values); err == nil {
			parsed, err = f.fromStr(raw)
		}
	}
	if err != nil {
		return false, fmt.Errorf("error parsing command-line argument --%s: %w", f.name, err)
	}
	if err := f.apply(s, parsed); err != nil {
		return false, fmt.Errorf("error applying command-line argument --%s: %w", f.name, err)
	}
	return true, nil
}

func strFlag(name, short, env, desc, defVal string, set func(*Settings, string)) settingDef {
	return &flagDefImpl[string]{
		name:    name,
		short:   short,
		env:     env,
		desc:    desc,
		defVal:  new(defVal),
		fromStr: strFromStr,
		apply:   func(s *Settings, v string) error { set(s, v); return nil },
	}
}

func intFlag(name, env, desc string, defVal int, set func(*Settings, int)) settingDef {
	return &flagDefImpl[int]{
		name:    name,
		env:     env,
		desc:    desc,
		defVal:  new(defVal),
		fromStr: strconv.Atoi,
		apply:   func(s *Settings, v int) error { set(s, v); return nil },
	}
}

func durationFlag(name, env, desc string, defVal time.Duration, set func(*Settings, time.Duration)) settingDef {
	return &flagDefImpl[time.Duration]{
		name:    name,
		env:     env,
		desc:    desc,
		defVal:  new(defVal),
		fromStr: time.ParseDuration,
		apply:   func(s *Settings, v time.Duration) error { set(s, v); return nil },
	}
}

func boolFlag(name, env, desc string, defVal bool, set func(*Settings, bool)) settingDef {
	return &flagDefImpl[bool]{
		name:       name,
		env:        env,
		desc:       desc,
		defVal:     new(defVal),
		fromStr:    strconv.ParseBool,
		fromStrArr: boolFromStrLastArg,
		apply:      func(s *Settings, v bool) error { set(s, v); return nil },
	}
}

func strSliceFlag(name, env, desc string, set func(*Settings, []string)) settingDef {
	return &flagDefImpl[[]string]{
		name:       name,
		env:        env,
		desc:       desc,
		defVal:     &[]string{},
		fromStr:    strArrFromStr,
		fromStrArr: strArrFromStrArr,
		apply:      func(s *Settings, v []string) error { set(s, v); return nil },
	}
}

var allSettingDefs = []settingDef{
	strFlag("instruction-path", "", "AG_INSTRUCTION_PATH",
		"Path to a Markdown file whose contents are prepended to the system prompt",
		defaultInstructionPath, func(s *Settings, v string) { s.InstructionPath = v }),
	strFlag("log-path", "", "AG_LOG_PATH",
		"Log file path",
		defaultLogPath, func(s *Settings, v string) { s.LogFilePath = v }),
	boolFlag("logging-enabled", "AG_LOGGING_ENABLED",
		"Enable logging to file. By default, logging is disabled. If enabled, logs will be written to the path specified by --log-path.",
		false, func(s *Settings, v bool) { s.LoggingEnabled = v }),
	strFlag("session-path", "", "AG_SESSION_PATH",
		"Path to a JSON file that stores mapping of session keys to context IDs",
		defaultSessionPath, func(s *Settings, v string) { s.SessionPath = v }),
	strFlag("session-key", "s", "AG_SESSION_KEY",
		"Specify a session key to use for this run or resume a previous session, session key is mapped to a context ID and would be updated on conversation turn.",
		"", func(s *Settings, v string) { s.SessionKey = v }),
	strFlag("context-dir", "", "AG_CONTEXT_DIR",
		"Directory to store conversation contexts.",
		defaultContextDir, func(s *Settings, v string) { s.ContextDir = v }),
	strFlag("context-id", "c", "AG_CONTEXT_ID",
		"Context ID from a previous run, to continue a prior conversation",
		"", func(s *Settings, v string) { s.ContextId = v }),
	durationFlag("context-ttl", "AG_CONTEXT_TTL",
		"Time-to-live for conversation contexts. Contexts older than this duration will be automatically deleted.",
		defaultContextTtl, func(s *Settings, v time.Duration) { s.ContextTtl = v }),
	strSliceFlag("skill-paths", "AG_SKILL_PATHS",
		"comma-separated list of paths to search for skill definitions. Paths can be directories or SKILL.md files. Directories are searched recursively for SKILL.md files.",
		func(s *Settings, v []string) { s.SkillPaths = append(s.SkillPaths, v...) }),
	strSliceFlag("preload-skill-paths", "AG_PRELOAD_SKILL_PATHS",
		"like skill-paths but loaded during agent initialization instead of on demand. Use this for skills that must be available from the start.",
		func(s *Settings, v []string) { s.PreloadSkillPaths = append(s.PreloadSkillPaths, v...) }),
	strSliceFlag("mcp-conf-paths", "AG_MCP_CONF_PATHS",
		"comma-separated list of paths to MCP config files. See: https://gofastmcp.com/integrations/mcp-json-configuration",
		func(s *Settings, v []string) { s.McpConfPaths = append(s.McpConfPaths, v...) }),
	strSliceFlag("a2a-urls", "AG_A2A_URLS",
		"comma-separated list of a2a urls. See: https://a2a-protocol.org/latest/",
		func(s *Settings, v []string) { s.A2aUrls = append(s.A2aUrls, v...) }),
	strFlag("history-file-path", "", "AG_HISTORY_FILE_PATH",
		"Directory to store historical user input",
		defaultHistoryFilePath, func(s *Settings, v string) { s.HistoryFilePath = v }),
	intFlag("history-max-count", "AG_HISTORY_MAX_COUNT",
		"Maximum number of historical user input to keep",
		defaultMaxHistoryCount, func(s *Settings, v int) { s.HistoryMaxCount = v }),
	intFlag("max-messages-before-user-prompt", "AG_MAX_MESSAGES_BEFORE_USER_PROMPT",
		"Maximum number of messages to keep before the last user prompt, older messages will be summarized and replaced with a summary message",
		defaultMaxMessagesBeforeUserPrompt, func(s *Settings, v int) { s.MaxMessagesBeforeUserPrompt = v }),
	intFlag("max-messages-after-user-prompt", "AG_MAX_MESSAGES_AFTER_USER_PROMPT",
		"Maximum number of messages to keep after the last user prompt, older messages will be summarized and replaced with a summary message",
		defaultMaxMessagesAfterUserPrompt, func(s *Settings, v int) { s.MaxMessagesAfterUserPrompt = v }),

	&flagDefImpl[string]{
		name:    "llm-provider",
		env:     "AG_LLM_PROVIDER",
		desc:    "LLM provider to use, e.g. 'anthropic', 'openai', or 'gemini'",
		fromStr: strFromStr,
		apply:   setLLmProvider,
	},

	strFlag("anthropic-base-url", "", "AG_ANTHROPIC_BASE_URL",
		"Anthropic API base URL",
		"", func(s *Settings, v string) { s.AnthropicBaseUrl = v }),
	strFlag("anthropic-model", "", "AG_ANTHROPIC_MODEL",
		"Anthropic model name, e.g. glm-5",
		"", func(s *Settings, v string) { s.AnthropicModel = v }),

	strFlag("openai-base-url", "", "AG_OPENAI_BASE_URL",
		"OpenAI-compatible API base URL",
		"", func(s *Settings, v string) { s.OpenAIBaseURL = v }),
	strFlag("openai-api-key", "", "AG_OPENAI_API_KEY",
		"OpenAI API key",
		"", func(s *Settings, v string) { s.OpenAIApiKey = v }),
	strFlag("openai-model", "", "AG_OPENAI_MODEL",
		"OpenAI model name, e.g. gpt-4o",
		"", func(s *Settings, v string) { s.OpenAIModel = v }),

	strFlag("gemini-base-url", "", "AG_GEMINI_BASE_URL",
		"Gemini API base URL",
		"", func(s *Settings, v string) { s.GeminiBaseURL = v }),
	strFlag("gemini-api-key", "", "AG_GEMINI_API_KEY",
		"Gemini API key (falls back to GEMINI_API_KEY / GOOGLE_API_KEY)",
		"", func(s *Settings, v string) { s.GeminiApiKey = v }),
	strFlag("gemini-model", "", "AG_GEMINI_MODEL",
		"Gemini model name, e.g. gemini-4-hydrogen",
		"", func(s *Settings, v string) { s.GeminiModel = v }),
}

func strFromStr(s string) (string, error) { return s, nil }

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

	flag := usageFlag{
		longName:     f.name,
		shortName:    f.short,
		envName:      f.env,
		expectsValue: !isBool,
		description:  f.desc,
		// An environment variable is an alternative way of supplying the
		// value, not a fallback for it, so only a default value can make a
		// setting optional. This mirrors the check in Init.
		required: f.defVal == nil,
	}

	if f.defVal != nil {
		if rendered, ok := formatDefaultValue(*f.defVal); ok {
			flag.defaultValue = rendered
			flag.hasDefaultValue = true
		}
	}

	return flag
}

func printHelp() {
	flags := []usageFlag{
		{
			longName:    "help",
			shortName:   "h",
			description: "Show this help message.",
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
		fmt.Fprintf(output, "      default: %s\n", flag.defaultValue)
	} else if flag.required {
		envHelpText := ""
		if flag.envName != "" {
			envHelpText = fmt.Sprintf(" (or set %s)", flag.envName)
		}
		fmt.Fprintf(output, "      required: yes%s\n", envHelpText)
	}
	fmt.Fprintln(output)
}

func flagLabel(flag usageFlag) string {
	label := "--" + flag.longName
	if strings.TrimSpace(flag.shortName) != "" {
		label = fmt.Sprintf("-%s, %s", flag.shortName, label)
	}
	if flag.expectsValue {
		label += " <" + flag.longName + ">"
	}
	return label
}

// formatDefaultValue renders a default value for the help output. The second
// return value reports whether the value is worth showing: several optional
// settings use a zero defVal (`new("")`, `&[]string{}`) merely to mark
// themselves as not required, and the zero value carries no information.
func formatDefaultValue(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, v != ""
	case []string:
		return strings.Join(v, ", "), len(v) > 0
	case time.Duration:
		return v.String(), v != 0
	case bool:
		// For a boolean flag, false is a meaningful value ("disabled"), not
		// a sign of a missing default.
		return strconv.FormatBool(v), true
	default:
		return fmt.Sprint(v), true
	}
}

type LllmProvider string

const (
	LlmProviderAnthropic LllmProvider = "anthropic"
	LlmProviderOpenAI    LllmProvider = "openai"
	LlmProviderGemini    LllmProvider = "gemini"
)

func setLLmProvider(p *Settings, provider string) error {
	switch LllmProvider(provider) {
	case LlmProviderAnthropic, LlmProviderOpenAI, LlmProviderGemini:
		p.LlmProvider = LllmProvider(provider)
		return nil
	default:
		return fmt.Errorf("unsupported llm provider: %s", provider)
	}
}
