# AGENTS.md

`ag` (`github.com/speed2exe/ag`) is a Go CLI coding agent supporting multiple LLM
providers (Anthropic, OpenAI Responses API, Gemini Interactions API), built-in
tools (`bash`, `agent`, `lua`, `compact`), MCP servers, A2A agents, Agent Skills
(`SKILL.md`), and `AGENTS.md` discovery.

## Build & Verify

Requires **Go 1.27+** (see `go.mod`). Default builds use cgo for GNU `readline`
and C `lua` (resolved via `pkg-config`). Pure-Go fallbacks exist for
`CGO_ENABLED=0` (`bufio.Scanner` for readline and `github.com/mmcdole/lunar` for
Lua).

### Nix

```sh
nix-shell -p lua pkg-config readline --run 'go build'
```

### macOS (Homebrew)

Requires Go plus the C dependencies for cgo (`readline` and `lua`, resolved via
`pkg-config`):

```sh
export PKG_CONFIG_PATH="/opt/homebrew/opt/readline/lib/pkgconfig:$PKG_CONFIG_PATH"
brew install go pkg-config readline lua
```

### Verification

Run both cgo and non-cgo builds and `go vet` before finishing changes (especially
when touching `readline/` or `tool/builtin/lua_*`):

```sh
go build ./... && go vet ./...
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./...
```

- Prefer `go build ./...` (or `go build -o /tmp/ag .`) over bare `go build` so a
  compiled `ag` binary isn't left in the repo root.
- There are currently no `*_test.go` files in the repository; if you add tests,
  verify them with `go test ./...`.

## Repository Layout

- `main.go` — CLI entrypoint, interactive/piped execution, session & prompt
  history persistence, tool wiring (`buildTools`), system instruction assembly
  (`buildInstructions`), and background context TTL cleanup.
- `core/` — Streaming agentic turn loop (`GenerateStream`), tool execution,
  context recording, automatic summarization (`MaxMessagesBeforeUserPrompt` /
  `MaxMessagesAfterUserPrompt`), and on-demand compaction (`RequestCompact`).
- `llm/` — `Model` interface (`llm.go`), streaming ANSI/Markdown terminal
  renderer (`handler.go`, `EventPrinter`), and provider clients:
  - `llm/anthropic/` — Anthropic Messages API
  - `llm/openai/` — OpenAI Responses API
  - `llm/gemini/` — Gemini Interactions API
- `models/` — Sealed `Event`, `Output`, and `Message` interfaces plus built-in
  tool input/output structs.
- `tool/` — `Tool` definition and implementations:
  - `tool/builtin/` — `bash`, `agent` (subagent), `lua` (cgo + `!cgo`), `compact`
  - `tool/mcp/` — MCP server config loader and tool adapters (`stdio`, `http`, `sse`)
  - `tool/a2a/` — A2A client discovery and `a2a_`-prefixed tools
  - `tool/skill/` — `SKILL.md` YAML frontmatter/preload loader and instruction builder
- `setting/` — Custom CLI flag and `AG_*` env var parser (`Init`, `StdinIsTerm`).
- `state/` — Request-scoped `context.Context` container (`Printer`, `Settings`,
  `Llm`, `ToolsByName`, `Skills`, `A2a`, `CtxStore`).
- `store/` — On-disk context persistence (`~/.ag/contexts/<rfc3339nano>/`,
  zero-padded JSON message files linked via `ContextId`).
- `agentsmd/` — Walks from working directory up to `.git` root collecting
  `AGENTS.md` files outermost-first.
- `printer/` — Thread-safe buffered ANSI terminal writer with indentation support.
- `readline/` — GNU readline (`cgo`) with `bufio.Scanner` (`!cgo`) fallback.
- `consts/` — Built-in tool name constants and summarization/compaction prompts.
- `util/` — `NewJSONTool` generic wrapper, stream helpers (`FeedChan`,
  `StreamActions`), and `glamour` Markdown/code rendering.
- `log/` — Optional file logger enabled via `--logging-enabled`.

## Architecture & Conventions

- **Go 1.27 idioms & `encoding/json/v2`**: Code uses Go 1.27 stdlib features
  including `encoding/json/v2`, `errors.AsType`, `sync.WaitGroup.Go`,
  `new(expr)`, and `slices`/`maps` helpers. Prefer `encoding/json/v2` for JSON;
  only import `encoding/json` (aliased as `stdjson`) when interoperating with a
  third-party type that requires v1 semantics.
- **CGo / `!cgo` parity**: `readline/` (`readline_cgo.go`, `readline_nocgo.go`)
  and `tool/builtin/` (`lua_cgo.go`, `lua_cgo_callback.go`, `lua_nocgo.go`, with
  shared logic in `lua_bindings.go`) maintain paired build-tagged files. Keep
  exported signatures and behavior consistent across both modes.
- **Adding `models.Message` or `models.Event` types**:
  - New `models.Message` types must be handled in `store/ctx_store.go`
    (`Save`/`Load`) and all three providers (`llm/anthropic`, `llm/openai`,
    `llm/gemini`) — all of them panic on unknown message types.
  - New `models.Event` types should be checked against `core/core.go`
    (`contextRecorder`, `outputAccumulator`, `toolCallExtractor`) and
    `llm/handler.go` (`EventPrinter`).
- **Adding a built-in tool**: Define input/output structs in `models/`, add the
  name constant in `consts/`, build it with `util.NewJSONTool` in `tool/builtin/`
  (JSON schema is derived automatically via `github.com/google/jsonschema-go/jsonschema`
  from `json` and `jsonschema` struct tags), and register it in `buildTools` in
  `main.go`.
- **Prompt cache determinism**: Keep tools (`util.ToolSortedListFromMap`) and
  instruction/skill lists sorted deterministically so system prompt prefixes
  stay cacheable across turns.
- **Subagent isolation**: `tool/builtin/agent.go` isolates subagents by
  shallow-copying `state.State` with scoped `ToolsByName`/`Skills` and indenting
  `st.Printer`. Do not mutate maps on a shared parent `state.State`.
- **Settings & CLI flags**: `setting/setting.go` uses a custom flag parser (not
  stdlib `flag`). Register new options in `(*Settings).flagDefs()` with both a
  `--flag` and an `AG_*` env var (CLI flags override env vars).
