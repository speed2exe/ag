package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"encoding/json/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/speed2exe/ag/tool"
)

type McpFromConfig struct {
	toolsByMcp map[string][]*tool.Tool
}

// ServerConfig represents a single MCP server entry in an mcp.json config file.
// Exactly one of URL (Streamable HTTP / SSE transport) or Command (stdio transport) must be set.
// Type selects between "http" (Streamable HTTP, default when url is set) and "sse" (legacy SSE).
type ServerConfig struct {
	Path string `json:"-"` // internal: the file path from which this config was loaded, for logging purposes
	Name string `json:"-"` // internal: the server name (map key), for logging purposes

	// HTTP/SSE transport: connect to an already-running remote server.
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`

	// Stdio transport: launch a local subprocess.
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`

	// Type selects the transport: "stdio", "http" (Streamable HTTP), or "sse" (legacy SSE 2024-11-05).
	// When omitted, defaults to "stdio" if command is set, or "http" if url is set.
	Type string `json:"type"`
}

// Config represents the top-level structure of an mcp json config.
type Config struct {
	MCPServers map[string]*ServerConfig `json:"mcpServers"`
}

func NewMcpFromConfig(ctx context.Context, log func(string), configPaths []string) map[string][]*tool.Tool {
	cfg := loadMcpConfig(log, configPaths)
	impl := &mcp.Implementation{}

	mu := sync.Mutex{}
	toolsByServeName := map[string][]*tool.Tool{}

	var wg sync.WaitGroup
	for serverName, cfg := range cfg.MCPServers {
		wg.Go(func() {
			tools, err := toolsFromServer(ctx, log, cfg, impl)
			if err != nil {
				log(fmt.Sprintf("warning: failed to initialize MCP server %q: %v; skipping\n", serverName, err))
				return
			}

			mu.Lock()
			toolsByServeName[serverName] = tools
			mu.Unlock()
		})
	}
	wg.Wait()

	return toolsByServeName
}

func toolsFromServer(ctx context.Context, log func(string), cfg *ServerConfig, impl *mcp.Implementation) ([]*tool.Tool, error) {
	var transport mcp.Transport

	switch {
	case cfg.URL != "" && cfg.Command != "":
		return nil, errors.New("only one of \"url\" or \"command\" may be set")
	case cfg.URL != "":
		switch strings.ToLower(cfg.Type) {
		case "sse":
			transport = &mcp.SSEClientTransport{
				Endpoint:   cfg.URL,
				HTTPClient: httpClientWithHeaders(cfg.Headers),
			}
		default: // "http" or unset
			transport = &mcp.StreamableClientTransport{
				Endpoint:   cfg.URL,
				HTTPClient: httpClientWithHeaders(cfg.Headers),
			}
		}
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = buildEnv(cfg.Env)
		transport = &mcp.CommandTransport{Command: cmd}
	default:
		return nil, errors.New("one of \"url\" or \"command\" must be set")
	}

	client := mcp.NewClient(impl, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	tools := make([]*tool.Tool, 0, len(result.Tools))
	for _, mt := range result.Tools {
		t, err := convertGoSDKTool(session, cfg, mt)
		if err != nil {
			log(fmt.Sprintf("warning: MCP config server %q: failed to convert tool %q: %v; skipping\n", cfg.Name, mt.Name, err))
			continue
		}
		tools = append(tools, t)
	}

	log(fmt.Sprintf("loaded MCP server from file: %s %q (%d tools)\n", cfg.Path, cfg.Name, len(result.Tools)))
	return tools, nil
}

// httpClientWithHeaders returns an *http.Client that injects the given headers
// into every request. Returns nil (use default client) when headers is empty.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{
		Transport: &headerRoundTripper{
			headers: headers,
			base:    http.DefaultTransport,
		},
	}
}

// headerRoundTripper injects static headers into every outgoing HTTP request.
type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range rt.headers {
		req.Header.Set(k, v)
	}
	return rt.base.RoundTrip(req)
}

func convertGoSDKTool(session *mcp.ClientSession, cfg *ServerConfig, mt *mcp.Tool) (*tool.Tool, error) {
	schema, err := json.Marshal(mt.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input schema: %w", err)
	}
	desc := mt.Description
	return &tool.Tool{
		Name:        fmt.Sprintf("%s@%s:%s", cfg.Name, cfg.Path, mt.Name),
		Description: &desc,
		InputSchema: schema,
		Call:        sessionCallFunc(session, mt.Name),
	}, nil
}

func sessionCallFunc(session *mcp.ClientSession, name string) func(ctx context.Context, args []byte) ([]byte, error) {
	return func(ctx context.Context, args []byte) ([]byte, error) {
		var arguments map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &arguments); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool arguments: %w", err)
			}
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      name,
			Arguments: arguments,
		})
		if err != nil {
			return nil, fmt.Errorf("MCP tool call failed: %w", err)
		}
		if result.IsError {
			return nil, fmt.Errorf("MCP tool returned error: %s", extractGoSDKText(result.Content))
		}
		return []byte(extractGoSDKText(result.Content)), nil
	}
}

func extractGoSDKText(contents []mcp.Content) string {
	var sb strings.Builder
	for _, c := range contents {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// loadMcpConfig reads mcp json config from file paths
// merging them with project-level entries taking
// newer order precedence over older ones. Returns a merged Config.
func loadMcpConfig(log func(string), filePaths []string) *Config {
	configs := make([]*Config, 0, len(filePaths))

	for _, name := range filePaths {
		cfg, err := loadConfig(name)
		if err != nil {
			log(fmt.Sprintf("warning: failed to load MCP config %q: %v; skipping\n", name, err))
			continue
		}
		configs = append(configs, cfg)
	}

	if len(configs) == 0 {
		return &Config{MCPServers: make(map[string]*ServerConfig)}
	} else if len(configs) == 1 {
		return configs[0]
	}

	merged := configs[0]

	// Merge configs in order, with later configs overriding earlier ones.
	for _, cfg := range configs[1:] {
		maps.Copy(merged.MCPServers, cfg.MCPServers)
	}

	return merged
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read MCP config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MCP config %q: %w", path, err)
	}
	for k, v := range cfg.MCPServers {
		v.Name = k
		v.Path = path
		cfg.MCPServers[k] = v
	}
	return &cfg, nil
}

// buildEnv merges the current process environment with the extra key=value
// pairs from the server config, returning a combined slice suitable for exec.Cmd.
func buildEnv(extra map[string]string) []string {
	base := os.Environ()
	result := make([]string, len(base), len(base)+len(extra))
	copy(result, base)
	for k, v := range extra {
		result = append(result, k+"="+v)
	}
	return result
}
