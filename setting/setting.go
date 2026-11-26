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

type Mode int

const (
	ModeCLI Mode = iota
	ModeA2AServer
)

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

	// A2A server settings (used by cmd/a2a).
	A2aServerAddr              string
	A2aServerGrpcAddr          string
	A2aServerPublicURL         string
	A2aServerGrpcURL           string
	A2aServerDataDir           string
	A2aServerName              string
	A2aServerDescription       string
	A2aServerVersion           string
	A2aServerProviderOrg       string
	A2aServerProviderURL       string
	A2aServerIconURL           string
	A2aServerDocURL            string
	A2aServerCardPath          string
	A2aServerExtendedCardPath  string
	A2aServerInputModes        []string
	A2aServerOutputModes       []string
	A2aServerBearerToken       string
	A2aServerApiKey            string
	A2aServerApiKeyHeader      string
	A2aServerCompatV0          bool
	A2aServerAllowPrivatePush  bool
	A2aServerKeepAlive         time.Duration
	A2aServerInactivityTimeout time.Duration
	A2aServerMaxConcurrent     int
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

		A2aServerAddr:         ":8080",
		A2aServerDataDir:      filepath.Join(homeDir, ".ag", "a2a"),
		A2aServerName:         "ag",
		A2aServerDescription:  "Autonomous coding and task execution agent powered by ag",
		A2aServerVersion:      "1.0.0",
		A2aServerInputModes:   []string{"text/plain", "text/markdown", "application/json", "application/octet-stream"},
		A2aServerOutputModes:  []string{"text/plain", "text/markdown", "application/json"},
		A2aServerApiKeyHeader: "X-API-Key",
		A2aServerCompatV0:     true,
		A2aServerKeepAlive:    15 * time.Second,
	}
}

func Init() (*Settings, error) {
	return initWithMode(ModeCLI)
}

func InitA2AServer() (*Settings, error) {
	return initWithMode(ModeA2AServer)
}

func initWithMode(mode Mode) (*Settings, error) {
	p := defaultSettings()
	defs := p.flagDefsForMode(mode)

	if slices.ContainsFunc(os.Args[1:], func(arg string) bool { return arg == "-h" || arg == "--help" }) {
		printHelp(defs, mode)
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

	if mode == ModeCLI {
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
	return s.flagDefsForMode(ModeCLI)
}

func (s *Settings) flagDefsForMode(mode Mode) []flagDef {
	defs := s.commonFlagDefs()
	switch mode {
	case ModeCLI:
		defs = append(defs, s.cliFlagDefs()...)
	case ModeA2AServer:
		defs = append(defs, s.a2aServerFlagDefs()...)
	}
	return defs
}

func (s *Settings) commonFlagDefs() []flagDef {
	return []flagDef{
		strFlag(&s.InstructionPath, "instruction-path", "", "AG_INSTRUCTION_PATH",
			"Path to a Markdown file whose contents are prepended to the system prompt"),
		strFlag(&s.LogFilePath, "log-path", "", "AG_LOG_PATH",
			"Log file path"),
		boolFlag(&s.LoggingEnabled, "logging-enabled", "AG_LOGGING_ENABLED",
			"Enable logging to file. By default, logging is disabled. If enabled, logs will be written to the path specified by --log-path."),
		strFlag(&s.ContextDir, "context-dir", "", "AG_CONTEXT_DIR",
			"Directory to store conversation contexts."),
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

func (s *Settings) cliFlagDefs() []flagDef {
	return []flagDef{
		strFlag(&s.SessionPath, "session-path", "", "AG_SESSION_PATH",
			"Path to a JSON file that stores mapping of session keys to context IDs"),
		strFlag(&s.SessionKey, "session-key", "s", "AG_SESSION_KEY",
			"Specify a session key to use for this run or resume a previous session, session key is mapped to a context ID and would be updated on conversation turn."),
		strFlag(&s.ContextId, "context-id", "c", "AG_CONTEXT_ID",
			"Context ID from a previous run, to continue a prior conversation"),
		strFlag(&s.HistoryFilePath, "history-file-path", "", "AG_HISTORY_FILE_PATH",
			"Directory to store historical user input"),
		intFlag(&s.HistoryMaxCount, "history-max-count", "AG_HISTORY_MAX_COUNT",
			"Maximum number of historical user input to keep"),
	}
}

func (s *Settings) a2aServerFlagDefs() []flagDef {
	return []flagDef{
		strFlag(&s.A2aServerAddr, "a2a-addr", "", "AG_A2A_ADDR",
			"HTTP listen address for A2A JSON-RPC, HTTP+JSON REST, and Agent Card endpoints"),
		strFlag(&s.A2aServerGrpcAddr, "a2a-grpc-addr", "", "AG_A2A_GRPC_ADDR",
			"Optional gRPC listen address for A2A gRPC transport (e.g. ':9090'; disabled if empty)"),
		strFlag(&s.A2aServerPublicURL, "a2a-url", "", "AG_A2A_URL",
			"Public HTTP base URL advertised in the Agent Card (defaults to http://localhost:<port> from --a2a-addr)"),
		strFlag(&s.A2aServerGrpcURL, "a2a-grpc-url", "", "AG_A2A_GRPC_URL",
			"Public gRPC endpoint advertised in the Agent Card (defaults to localhost:<port> from --a2a-grpc-addr)"),
		strFlag(&s.A2aServerDataDir, "a2a-data-dir", "", "AG_A2A_DATA_DIR",
			"Directory to persist A2A tasks, push notification configs, context mappings, and uploaded files"),
		strFlag(&s.A2aServerName, "a2a-name", "", "AG_A2A_NAME",
			"Agent name advertised in the Agent Card"),
		strFlag(&s.A2aServerDescription, "a2a-description", "", "AG_A2A_DESCRIPTION",
			"Agent description advertised in the Agent Card"),
		strFlag(&s.A2aServerVersion, "a2a-version", "", "AG_A2A_VERSION",
			"Agent version advertised in the Agent Card"),
		strFlag(&s.A2aServerProviderOrg, "a2a-provider-org", "", "AG_A2A_PROVIDER_ORG",
			"Optional organization name for AgentCard.provider.organization"),
		strFlag(&s.A2aServerProviderURL, "a2a-provider-url", "", "AG_A2A_PROVIDER_URL",
			"Optional URL for AgentCard.provider.url"),
		strFlag(&s.A2aServerIconURL, "a2a-icon-url", "", "AG_A2A_ICON_URL",
			"Optional icon URL advertised in the Agent Card"),
		strFlag(&s.A2aServerDocURL, "a2a-doc-url", "", "AG_A2A_DOC_URL",
			"Optional documentation URL advertised in the Agent Card"),
		strFlag(&s.A2aServerCardPath, "a2a-card-path", "", "AG_A2A_CARD_PATH",
			"Optional path to a JSON Agent Card file to customize/override the public Agent Card"),
		strFlag(&s.A2aServerExtendedCardPath, "a2a-extended-card-path", "", "AG_A2A_EXTENDED_CARD_PATH",
			"Optional path to a JSON Agent Card file for the extended authenticated Agent Card"),
		strSliceReplaceFlag(&s.A2aServerInputModes, "a2a-input-modes", "AG_A2A_INPUT_MODES",
			"Comma-separated list of supported input MIME types declared in the Agent Card"),
		strSliceReplaceFlag(&s.A2aServerOutputModes, "a2a-output-modes", "AG_A2A_OUTPUT_MODES",
			"Comma-separated list of supported output MIME types declared in the Agent Card"),
		strFlag(&s.A2aServerBearerToken, "a2a-bearer-token", "", "AG_A2A_BEARER_TOKEN",
			"Optional Bearer token required to authenticate A2A requests (Authorization: Bearer <token>)"),
		strFlag(&s.A2aServerApiKey, "a2a-api-key", "", "AG_A2A_API_KEY",
			"Optional API key required to authenticate A2A requests"),
		strFlag(&s.A2aServerApiKeyHeader, "a2a-api-key-header", "", "AG_A2A_API_KEY_HEADER",
			"HTTP header / gRPC metadata name for API key authentication"),
		boolFlag(&s.A2aServerCompatV0, "a2a-compat-v0", "AG_A2A_COMPAT_V0",
			"Enable A2A v0.3 compatibility endpoints (/.well-known/agent.json, /v0/jsonrpc, /v1/*, v0 gRPC)"),
		boolFlag(&s.A2aServerAllowPrivatePush, "a2a-allow-private-push", "AG_A2A_ALLOW_PRIVATE_PUSH",
			"Allow push notification webhooks targeting loopback/private networks (disabled by default for SSRF protection)"),
		durationFlag(&s.A2aServerKeepAlive, "a2a-keep-alive", "AG_A2A_KEEP_ALIVE",
			"SSE keep-alive interval for streaming responses (0 disables keep-alive)"),
		durationFlag(&s.A2aServerInactivityTimeout, "a2a-inactivity-timeout", "AG_A2A_INACTIVITY_TIMEOUT",
			"Optional inactivity timeout for agent task execution (0 disables timeout)"),
		intFlag(&s.A2aServerMaxConcurrent, "a2a-max-concurrent-tasks", "AG_A2A_MAX_CONCURRENT_TASKS",
			"Maximum number of concurrent task executions (0 means unlimited)"),
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

func strSliceReplaceFlag(dst *[]string, name, env, desc string) flagDef {
	defDisplay := strings.Join(*dst, ",")
	parseSplit := func(s string) []string {
		var out []string
		for p := range strings.SplitSeq(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	return flagDef{
		name:       name,
		env:        env,
		desc:       desc,
		defDisplay: defDisplay,
		parse: func(v string) error {
			*dst = parseSplit(v)
			return nil
		},
		applyArgs: func(values []*string) error {
			var out []string
			for _, v := range values {
				if v != nil {
					out = append(out, parseSplit(*v)...)
				}
			}
			*dst = out
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

func printHelp(defs []flagDef, mode Mode) {
	out := os.Stdout
	fmt.Fprintf(out, "Usage: %s [options]\n\n", os.Args[0])
	switch mode {
	case ModeA2AServer:
		fmt.Fprintln(out, "Description:\n  Run ag as an A2A (Agent2Agent) protocol server.\n\nOptions:")
	default:
		fmt.Fprintln(out, "Input:\n  Read the user message from stdin.\n\nOptions:")
	}
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
