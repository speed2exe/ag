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

var StdinIsTerm = term.IsTerminal(int(os.Stdin.Fd()))

type LlmProvider string

const (
	LlmProviderAnthropic LlmProvider = "anthropic"
	LlmProviderOpenAI    LlmProvider = "openai"
	LlmProviderGemini    LlmProvider = "gemini"
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

	UserInput string

	LlmProvider LlmProvider

	AnthropicBaseUrl string
	AnthropicModel   string

	OpenAIBaseURL string
	OpenAIApiKey  string
	OpenAIModel   string

	GeminiBaseURL string
	GeminiApiKey  string
	GeminiModel   string
}

func defaultSettings() Settings {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Errorf("error getting user home directory: %v", err)
	}
	return Settings{
		LogFilePath:                 "./ag.log",
		InstructionPath:             filepath.Join(homeDir, ".ag", "instruction.md"),
		SessionPath:                 filepath.Join(homeDir, ".ag", "sessions.json"),
		ContextDir:                  filepath.Join(homeDir, ".ag", "contexts"),
		ContextTtl:                  7 * 24 * time.Hour,
		HistoryFilePath:             filepath.Join(homeDir, ".ag", "history.json"),
		HistoryMaxCount:             128,
		MaxMessagesBeforeUserPrompt: 32,
		MaxMessagesAfterUserPrompt:  64,
	}
}

func Init() (*Settings, error) {
	p := defaultSettings()
	defs := p.flagDefs()

	if slices.ContainsFunc(os.Args[1:], func(arg string) bool { return arg == "-h" || arg == "--help" }) {
		printHelp(defs)
		os.Exit(0)
	}

	cliArgs, err := parseCliArgs(os.Args[1:], defs)
	if err != nil {
		return nil, fmt.Errorf("error initializing settings: error parsing command-line arguments: %w", err)
	}

	for _, f := range defs {
		applied := false
		if f.env != "" {
			if value, ok := os.LookupEnv(f.env); ok {
				if err := f.parse(value); err != nil {
					return nil, fmt.Errorf("error initializing settings: error parsing environment variable %s: %w", f.env, err)
				}
				applied = true
			}
		}
		if values, ok := cliArgs[f.name]; ok {
			if err := f.applyArgs(values); err != nil {
				return nil, fmt.Errorf("error initializing settings: error parsing command-line argument --%s: %w", f.name, err)
			}
			applied = true
		}
		if f.required && !applied {
			envHelp := ""
			if f.env != "" {
				envHelp = " or environment variable " + f.env
			}
			return nil, fmt.Errorf("error initializing settings: missing required setting: --%s%s", f.name, envHelp)
		}
	}

	if !StdinIsTerm {
		bs, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("error initializing settings: error reading user input from stdin: %w", err)
		}
		p.UserInput = string(bs)
	}

	if p.SessionKey != "" && p.ContextId != "" {
		return nil, fmt.Errorf("error initializing settings: cannot specify both --session-key (AG_SESSION_KEY) and --context-id (AG_CONTEXT_ID); use one or the other")
	}

	return &p, nil
}

type flagDef struct {
	name       string
	short      string
	env        string
	desc       string
	defDisplay string
	required   bool
	isBool     bool
	parse      func(string) error
	applyArgs  func([]*string) error
}

func (s *Settings) flagDefs() []flagDef {
	return []flagDef{
		strFlag(&s.InstructionPath, "instruction-path", "", "AG_INSTRUCTION_PATH",
			"Path to a Markdown file whose contents are prepended to the system prompt"),
		strFlag(&s.LogFilePath, "log-path", "", "AG_LOG_PATH",
			"Log file path"),
		boolFlag(&s.LoggingEnabled, "logging-enabled", "AG_LOGGING_ENABLED",
			"Enable logging to file. By default, logging is disabled. If enabled, logs will be written to the path specified by --log-path."),
		strFlag(&s.SessionPath, "session-path", "", "AG_SESSION_PATH",
			"Path to a JSON file that stores mapping of session keys to context IDs"),
		strFlag(&s.SessionKey, "session-key", "s", "AG_SESSION_KEY",
			"Specify a session key to use for this run or resume a previous session, session key is mapped to a context ID and would be updated on conversation turn."),
		strFlag(&s.ContextDir, "context-dir", "", "AG_CONTEXT_DIR",
			"Directory to store conversation contexts."),
		strFlag(&s.ContextId, "context-id", "c", "AG_CONTEXT_ID",
			"Context ID from a previous run, to continue a prior conversation"),
		durationFlag(&s.ContextTtl, "context-ttl", "AG_CONTEXT_TTL",
			"Time-to-live for conversation contexts. Contexts older than this duration will be automatically deleted."),
		strSliceFlag(&s.SkillPaths, "skill-paths", "AG_SKILL_PATHS",
			"comma-separated list of paths to search for skill definitions. Paths can be directories or SKILL.md files. Directories are searched recursively for SKILL.md files."),
		strSliceFlag(&s.PreloadSkillPaths, "preload-skill-paths", "AG_PRELOAD_SKILL_PATHS",
			"like skill-paths but loaded during agent initialization instead of on demand. Use this for skills that must be available from the start."),
		strSliceFlag(&s.McpConfPaths, "mcp-conf-paths", "AG_MCP_CONF_PATHS",
			"comma-separated list of paths to MCP config files. See: https://gofastmcp.com/integrations/mcp-json-configuration"),
		strSliceFlag(&s.A2aUrls, "a2a-urls", "AG_A2A_URLS",
			"comma-separated list of a2a urls. See: https://a2a-protocol.org/latest/"),
		strFlag(&s.HistoryFilePath, "history-file-path", "", "AG_HISTORY_FILE_PATH",
			"Directory to store historical user input"),
		intFlag(&s.HistoryMaxCount, "history-max-count", "AG_HISTORY_MAX_COUNT",
			"Maximum number of historical user input to keep"),
		intFlag(&s.MaxMessagesBeforeUserPrompt, "max-messages-before-user-prompt", "AG_MAX_MESSAGES_BEFORE_USER_PROMPT",
			"Maximum number of messages to keep before the last user prompt, older messages will be summarized and replaced with a summary message"),
		intFlag(&s.MaxMessagesAfterUserPrompt, "max-messages-after-user-prompt", "AG_MAX_MESSAGES_AFTER_USER_PROMPT",
			"Maximum number of messages to keep after the last user prompt, older messages will be summarized and replaced with a summary message"),
		valueFlag("llm-provider", "", "AG_LLM_PROVIDER",
			"LLM provider to use, e.g. 'anthropic', 'openai', or 'gemini'", "", true,
			func(v string) error {
				switch LlmProvider(v) {
				case LlmProviderAnthropic, LlmProviderOpenAI, LlmProviderGemini:
					s.LlmProvider = LlmProvider(v)
					return nil
				default:
					return fmt.Errorf("unsupported llm provider: %s", v)
				}
			}),
		strFlag(&s.AnthropicBaseUrl, "anthropic-base-url", "", "AG_ANTHROPIC_BASE_URL",
			"Anthropic API base URL"),
		strFlag(&s.AnthropicModel, "anthropic-model", "", "AG_ANTHROPIC_MODEL",
			"Anthropic model name, e.g. glm-5"),
		strFlag(&s.OpenAIBaseURL, "openai-base-url", "", "AG_OPENAI_BASE_URL",
			"OpenAI-compatible API base URL"),
		strFlag(&s.OpenAIApiKey, "openai-api-key", "", "AG_OPENAI_API_KEY",
			"OpenAI API key"),
		strFlag(&s.OpenAIModel, "openai-model", "", "AG_OPENAI_MODEL",
			"OpenAI model name, e.g. gpt-4o"),
		strFlag(&s.GeminiBaseURL, "gemini-base-url", "", "AG_GEMINI_BASE_URL",
			"Gemini API base URL"),
		strFlag(&s.GeminiApiKey, "gemini-api-key", "", "AG_GEMINI_API_KEY",
			"Gemini API key (falls back to GEMINI_API_KEY / GOOGLE_API_KEY)"),
		strFlag(&s.GeminiModel, "gemini-model", "", "AG_GEMINI_MODEL",
			"Gemini model name, e.g. gemini-4-hydrogen"),
	}
}

func valueFlag(name, short, env, desc, defDisplay string, required bool, parse func(string) error) flagDef {
	return flagDef{
		name:       name,
		short:      short,
		env:        env,
		desc:       desc,
		defDisplay: defDisplay,
		required:   required,
		parse:      parse,
		applyArgs: func(values []*string) error {
			val := values[len(values)-1]
			if val == nil {
				return errors.New("expected value for flag but got none")
			}
			return parse(*val)
		},
	}
}

func strFlag(dst *string, name, short, env, desc string) flagDef {
	return valueFlag(name, short, env, desc, *dst, false, func(v string) error {
		*dst = v
		return nil
	})
}

func intFlag(dst *int, name, env, desc string) flagDef {
	return valueFlag(name, "", env, desc, strconv.Itoa(*dst), false, func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		*dst = n
		return nil
	})
}

func durationFlag(dst *time.Duration, name, env, desc string) flagDef {
	def := ""
	if *dst != 0 {
		def = dst.String()
	}
	return valueFlag(name, "", env, desc, def, false, func(v string) error {
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		*dst = d
		return nil
	})
}

func boolFlag(dst *bool, name, env, desc string) flagDef {
	parse := func(v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return err
		}
		*dst = b
		return nil
	}
	return flagDef{
		name:       name,
		env:        env,
		desc:       desc,
		defDisplay: strconv.FormatBool(*dst),
		isBool:     true,
		parse:      parse,
		applyArgs: func(values []*string) error {
			val := values[len(values)-1]
			if val == nil {
				*dst = true // flag present without a value → true
				return nil
			}
			return parse(*val)
		},
	}
}

func strSliceFlag(dst *[]string, name, env, desc string) flagDef {
	appendSplit := func(s string) {
		for p := range strings.SplitSeq(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				*dst = append(*dst, t)
			}
		}
	}
	return flagDef{
		name:  name,
		env:   env,
		desc:  desc,
		parse: func(v string) error { appendSplit(v); return nil },
		applyArgs: func(values []*string) error {
			for _, v := range values {
				if v != nil {
					appendSplit(*v)
				}
			}
			return nil
		},
	}
}

func parseCliArgs(argv []string, defs []flagDef) (map[string][]*string, error) {
	longByShort := make(map[string]string)
	knownLong := map[string]bool{"help": true}
	for _, def := range defs {
		knownLong[def.name] = true
		if def.short != "" {
			if _, exists := longByShort[def.short]; exists {
				panic(fmt.Sprintf("duplicate short option: -%s", def.short))
			}
			longByShort[def.short] = def.name
		}
	}

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
		} else if !knownLong[key] {
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

func printHelp(defs []flagDef) {
	out := os.Stdout
	fmt.Fprintf(out, "Usage: %s [options]\n\n", os.Args[0])
	fmt.Fprintln(out, "Input:\n  Read the user message from stdin.\n\nOptions:")
	all := append([]flagDef{{
		name:   "help",
		short:  "h",
		desc:   "Show this help message.",
		isBool: true,
	}}, defs...)
	for _, f := range all {
		label := "--" + f.name
		if f.short != "" {
			label = fmt.Sprintf("-%s, %s", f.short, label)
		}
		if !f.isBool {
			label += " <" + f.name + ">"
		}
		fmt.Fprintf(out, "  %s\n      %s\n", label, f.desc)
		if f.defDisplay != "" {
			fmt.Fprintf(out, "      default: %s\n", f.defDisplay)
		} else if f.required {
			envHelp := ""
			if f.env != "" {
				envHelp = fmt.Sprintf(" (or set %s)", f.env)
			}
			fmt.Fprintf(out, "      required: yes%s\n", envHelp)
		}
		fmt.Fprintln(out)
	}
}
