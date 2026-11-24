package a2a

import (
	"context"
	"crypto/tls"
	stdjson "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	a2acore "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	a2agrpcv0 "github.com/a2aproject/a2a-go/v2/a2agrpc/v0"
	a2agrpcv1 "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const legacyAgentCardPath = "/.well-known/agent.json"

var errInvalidAgentCard = errors.New("agent card has no supported interfaces")

type agent struct {
	URL    string             `json:"url"`
	Card   *a2acore.AgentCard `json:"agent_card"`
	client *a2aclient.Client
}

// Clients manages a collection of initialized A2A agent clients.
type Clients struct {
	agents []*agent
}

// New resolves agent cards and initializes A2A clients for each configured URL.
// Failed URLs emit warnings via log and are skipped.
func New(ctx context.Context, log func(string), urls []string) *Clients {
	seen := make(map[string]bool, len(urls))
	unique := make([]string, 0, len(urls))
	for _, u := range urls {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			unique = append(unique, u)
		}
	}
	if len(unique) == 0 {
		return &Clients{}
	}

	logf := func(format string, args ...any) {
		if log != nil {
			log(fmt.Sprintf(format, args...))
		}
	}

	resolver := &agentcard.Resolver{CardParser: compatCardParser()}
	factory := newClientFactory()

	results := make([]*agent, len(unique))
	var wg sync.WaitGroup
	for i, rawURL := range unique {
		wg.Go(func() {
			card, err := resolveCard(ctx, resolver, rawURL)
			if err != nil {
				logf("warning: failed to resolve A2A agent card %q: %v; skipping\n", rawURL, err)
				return
			}
			client, err := factory.CreateFromCard(ctx, card)
			if err != nil {
				logf("warning: failed to create A2A client for %q (%s): %v; skipping\n", rawURL, card.Name, err)
				return
			}
			results[i] = &agent{URL: rawURL, Card: card, client: client}
			logf("loaded A2A agent %q from %s (%d skills)\n", card.Name, rawURL, len(card.Skills))
		})
	}
	wg.Wait()

	c := &Clients{}
	for _, a := range results {
		if a != nil {
			c.agents = append(c.agents, a)
		}
	}
	return c
}

// Close destroys all underlying A2A client transports.
func (c *Clients) Close() {
	if c == nil {
		return
	}
	for _, a := range c.agents {
		if a.client != nil {
			_ = a.client.Destroy()
		}
	}
}

// BuildInstructions returns a system prompt fragment containing all resolved A2A agent cards.
func (c *Clients) BuildInstructions() string {
	if c == nil || len(c.agents) == 0 {
		return ""
	}
	bs, _ := json.Marshal(c.agents)
	return "You have access to the following A2A (Agent2Agent) remote agents. " +
		"Use the a2a_* tools with the agent's url to interact with them.\n" + string(bs)
}

func (c *Clients) resolveClient(target string) (*a2aclient.Client, error) {
	if c == nil || len(c.agents) == 0 {
		return nil, errors.New("no A2A agents configured")
	}

	key := strings.TrimRight(strings.TrimSpace(target), "/")
	for _, a := range c.agents {
		if strings.TrimRight(a.URL, "/") == key {
			return a.client, nil
		}
		for _, iface := range a.Card.SupportedInterfaces {
			if iface != nil && strings.TrimRight(iface.URL, "/") == key {
				return a.client, nil
			}
		}
	}

	trimmed := strings.TrimSpace(target)
	var matched *agent
	for _, a := range c.agents {
		if name := strings.TrimSpace(a.Card.Name); name != "" && name == trimmed {
			if matched != nil {
				return nil, fmt.Errorf("A2A agent name %q is ambiguous; specify one of: %s", target, c.summary())
			}
			matched = a
		}
	}
	if matched != nil {
		return matched.client, nil
	}

	return nil, fmt.Errorf("unknown A2A agent %q; available agents: %s", target, c.summary())
}

func (c *Clients) summary() string {
	parts := make([]string, 0, len(c.agents))
	for _, a := range c.agents {
		if a.Card.Name != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", a.URL, a.Card.Name))
		} else {
			parts = append(parts, a.URL)
		}
	}
	return strings.Join(parts, ", ")
}

// compatCardParser parses both A2A v1.0 and v0.3 agent cards.
// If a v0.3 card specifies "url" without "preferredTransport", it defaults to JSONRPC per the v0.3 spec.
func compatCardParser() agentcard.Parser {
	base := a2av0.NewAgentCardParser()
	return func(body []byte) (*a2acore.AgentCard, error) {
		card, err := base(body)
		if err != nil {
			return nil, err
		}
		if len(card.SupportedInterfaces) == 0 {
			var legacy struct {
				URL             string                  `json:"url"`
				ProtocolVersion a2acore.ProtocolVersion `json:"protocolVersion"`
			}
			if err := json.Unmarshal(body, &legacy); err == nil && legacy.URL != "" {
				ver := legacy.ProtocolVersion
				if ver == "" {
					ver = a2av0.Version
				}
				card.SupportedInterfaces = []*a2acore.AgentInterface{{
					URL:             legacy.URL,
					ProtocolBinding: a2acore.TransportProtocolJSONRPC,
					ProtocolVersion: ver,
				}}
			}
		}
		if len(card.SupportedInterfaces) == 0 {
			return nil, errInvalidAgentCard
		}
		return card, nil
	}
}

func resolveCard(ctx context.Context, resolver *agentcard.Resolver, rawURL string) (*a2acore.AgentCard, error) {
	card, err := resolver.Resolve(ctx, rawURL)
	if err == nil {
		return card, nil
	}

	// Fallback to the v0.3 well-known path (/.well-known/agent.json) for base URLs when
	// the default v1.0 path returned 404/405 or a non-card response.
	if u, parseErr := url.Parse(rawURL); parseErr == nil && u.Scheme != "file" && (u.Path == "" || u.Path == "/") && shouldFallbackToLegacyCardPath(err) {
		if legacyCard, legacyErr := resolver.Resolve(ctx, rawURL, agentcard.WithPath(legacyAgentCardPath)); legacyErr == nil {
			return legacyCard, nil
		}
	}
	return nil, err
}

func shouldFallbackToLegacyCardPath(err error) bool {
	if statusErr, ok := errors.AsType[*agentcard.ErrStatusNotOK](err); ok {
		return statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusMethodNotAllowed
	}
	_, isSyntax := errors.AsType[*stdjson.SyntaxError](err)
	return errors.Is(err, errInvalidAgentCard) || isSyntax
}

func newClientFactory() *a2aclient.Factory {
	return a2aclient.NewFactory(
		a2aclient.WithConfig(a2aclient.Config{
			PreferredTransports: []a2acore.TransportProtocol{
				a2acore.TransportProtocolJSONRPC,
				a2acore.TransportProtocolHTTPJSON,
				a2acore.TransportProtocolGRPC,
			},
		}),
		a2av0.WithJSONRPCTransport(a2av0.JSONRPCTransportConfig{}),
		a2av0.WithRESTTransport(a2av0.RESTTransportConfig{}),
		a2aclient.WithTransport(a2acore.TransportProtocolGRPC, grpcTransportFactory(a2agrpcv1.NewGRPCTransport)),
		a2aclient.WithCompatTransport(a2av0.Version, a2acore.TransportProtocolGRPC, grpcTransportFactory(a2agrpcv0.NewGRPCTransport)),
	)
}

func grpcTransportFactory(newTransport func(*grpc.ClientConn) a2aclient.Transport) a2aclient.TransportFactory {
	return a2aclient.TransportFactoryFn(func(ctx context.Context, card *a2acore.AgentCard, iface *a2acore.AgentInterface) (a2aclient.Transport, error) {
		target, creds := parseGRPCTarget(iface.URL)
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
		if err != nil {
			return nil, err
		}
		return newTransport(conn), nil
	})
}

func parseGRPCTarget(rawURL string) (string, credentials.TransportCredentials) {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "grpc":
			return u.Host, insecure.NewCredentials()
		case "https", "grpcs":
			return u.Host, credentials.NewTLS(&tls.Config{})
		}
	}
	host, _, err := net.SplitHostPort(rawURL)
	if err != nil {
		host = rawURL
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return rawURL, insecure.NewCredentials()
	}
	return rawURL, credentials.NewTLS(&tls.Config{})
}
