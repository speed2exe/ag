package mcp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/speed2exe/ag/tool"
)

type serverConfig struct {
	path string
	name string

	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`

	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`

	// "stdio", "http" (Streamable HTTP), or "sse" (legacy SSE).
	Type string `json:"type"`
}

type config struct {
	MCPServers map[string]*serverConfig `json:"mcpServers"`
}

func LoadTools(ctx context.Context, log func(string), configPaths []string) []*tool.Tool {
	servers := loadMcpConfig(log, configPaths)

	var (
		mu       sync.Mutex
		allTools []*tool.Tool
		wg       sync.WaitGroup
	)
	for serverName, cfg := range servers {
		wg.Go(func() {
			tools, err := toolsFromServer(ctx, log, cfg)
			if err != nil {
				log(fmt.Sprintf("warning: failed to initialize MCP server %q: %v; skipping\n", serverName, err))
				return
			}
			mu.Lock()
			allTools = append(allTools, tools...)
			mu.Unlock()
		})
	}
	wg.Wait()
	return allTools
}

func toolsFromServer(ctx context.Context, log func(string), cfg *serverConfig) ([]*tool.Tool, error) {
	var transport mcp.Transport
	switch {
	case cfg.URL != "" && cfg.Command != "":
		return nil, errors.New(`only one of "url" or "command" may be set`)
	case cfg.URL != "":
		httpClient := httpClientWithHeaders(cfg.Headers)
		if strings.EqualFold(cfg.Type, "sse") {
			transport = &mcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient}
		} else {
			transport = &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient}
		}
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = buildEnv(cfg.Env)
		transport = &mcp.CommandTransport{Command: cmd}
	default:
		return nil, errors.New(`one of "url" or "command" must be set`)
	}

	session, err := mcp.NewClient(&mcp.Implementation{}, nil).Connect(ctx, transport, nil)
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
			log(fmt.Sprintf("warning: MCP config server %q: failed to convert tool %q: %v; skipping\n", cfg.name, mt.Name, err))
			continue
		}
		tools = append(tools, t)
	}

	log(fmt.Sprintf("loaded MCP server from file: %s %q (%d tools)\n", cfg.path, cfg.name, len(result.Tools)))
	return tools, nil
}

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{Transport: headerRoundTripper(headers)}
}

type headerRoundTripper map[string]string

func (headers headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func convertGoSDKTool(session *mcp.ClientSession, cfg *serverConfig, mt *mcp.Tool) (*tool.Tool, error) {
	schema, err := json.Marshal(mt.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input schema: %w", err)
	}
	return &tool.Tool{
		Name:        fmt.Sprintf("%s_%s", cfg.name, mt.Name),
		Description: new(mt.Description),
		InputSchema: schema,
		Call: func(ctx context.Context, args []byte) ([]byte, error) {
			var arguments map[string]any
			if len(args) > 0 {
				if err := json.Unmarshal(args, &arguments); err != nil {
					return nil, fmt.Errorf("failed to unmarshal tool arguments: %w", err)
				}
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      mt.Name,
				Arguments: arguments,
			})
			if err != nil {
				return nil, fmt.Errorf("MCP tool call failed: %w", err)
			}
			text := extractGoSDKText(result.Content)
			if result.IsError {
				return nil, fmt.Errorf("MCP tool returned error: %s", text)
			}
			return []byte(text), nil
		},
	}, nil
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

func loadMcpConfig(log func(string), filePaths []string) map[string]*serverConfig {
	merged := make(map[string]*serverConfig)
	for _, path := range filePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			log(fmt.Sprintf("warning: failed to load MCP config %q: %v; skipping\n", path, err))
			continue
		}
		var cfg config
		if err := json.Unmarshal(data, &cfg); err != nil {
			log(fmt.Sprintf("warning: failed to load MCP config %q: %v; skipping\n", path, err))
			continue
		}
		for k, v := range cfg.MCPServers {
			if v == nil {
				delete(cfg.MCPServers, k)
				continue
			}
			v.name = k
			v.path = path
		}
		maps.Copy(merged, cfg.MCPServers)
	}
	return merged
}

func buildEnv(extra map[string]string) []string {
	result := os.Environ()
	for k, v := range extra {
		result = append(result, k+"="+v)
	}
	return result
}
