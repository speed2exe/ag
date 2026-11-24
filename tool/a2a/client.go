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
	"time"

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

// Agent holds a resolved A2A AgentCard and its connected client.
type Agent struct {
	URL    string             `json:"url"`
	Card   *a2acore.AgentCard `json:"agent_card"`
	Client *a2aclient.Client  `json:"-"`
}

// Clients manages a collection of initialized A2A agent clients.
type Clients struct {
	agents []*Agent
	byURL  map[string]*Agent
	byName map[string]*Agent // nil entry indicates an ambiguous agent name
}

// New resolves agent cards and initializes A2A clients for each configured URL.
// Failed URLs emit warnings via log and are skipped.
func New(ctx context.Context, log func(string), urls []string) *Clients {
	return newClients(ctx, log, urls, nil)
}

func newClients(ctx context.Context, log func(string), urls []string, httpClient *http.Client) *Clients {
	uniqueURLs := deduplicateURLs(urls)
	c := &Clients{
		byURL:  make(map[string]*Agent),
		byName: make(map[string]*Agent),
	}
	if len(uniqueURLs) == 0 {
		return c
	}

	resolver := newCardResolver(httpClient)
	factory := newClientFactory(httpClient)

	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		if log == nil {
			return
		}
		logMu.Lock()
		defer logMu.Unlock()
		log(fmt.Sprintf(format, args...))
	}

	results := make([]*Agent, len(uniqueURLs))
	var wg sync.WaitGroup
	for i, rawURL := range uniqueURLs {
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
			results[i] = &Agent{
				URL:    rawURL,
				Card:   card,
				Client: client,
			}
			logf("loaded A2A agent %q from %s (%d skills)\n", card.Name, rawURL, len(card.Skills))
		})
	}
	wg.Wait()

	for _, agent := range results {
		if agent == nil {
			continue
		}
		c.agents = append(c.agents, agent)
		c.indexAgent(agent)
	}
	return c
}

func (c *Clients) indexAgent(agent *Agent) {
	registerURL := func(u string) {
		key := normalizeLookupKey(u)
		if key == "" {
			return
		}
		if _, exists := c.byURL[key]; !exists {
			c.byURL[key] = agent
		}
	}

	registerURL(agent.URL)
	if agent.Card != nil {
		for _, iface := range agent.Card.SupportedInterfaces {
			if iface != nil {
				registerURL(iface.URL)
			}
		}
		if name := strings.TrimSpace(agent.Card.Name); name != "" {
			if _, exists := c.byName[name]; exists {
				c.byName[name] = nil // ambiguous name
			} else {
				c.byName[name] = agent
			}
		}
	}
}

// Agents returns all successfully initialized A2A agents in configured order.
func (c *Clients) Agents() []*Agent {
	if c == nil {
		return nil
	}
	return c.agents
}

// Close destroys all underlying A2A client transports.
func (c *Clients) Close() {
	if c == nil {
		return
	}
	for _, agent := range c.agents {
		if agent.Client != nil {
			_ = agent.Client.Destroy()
		}
	}
}

// BuildInstructions returns a system prompt fragment containing all resolved A2A agent cards.
func (c *Clients) BuildInstructions() string {
	if c == nil || len(c.agents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("You have access to the following A2A (Agent2Agent) remote agents. ")
	b.WriteString("Use the a2a_* tools with the agent's url to interact with them.\n")
	bs, _ := json.Marshal(c.agents)
	b.Write(bs)
	return b.String()
}

func (c *Clients) resolveClient(agentURL string) (*a2aclient.Client, error) {
	if c == nil || len(c.agents) == 0 {
		return nil, errors.New("no A2A agents configured")
	}

	key := normalizeLookupKey(agentURL)
	if agent, ok := c.byURL[key]; ok {
		return agent.Client, nil
	}

	trimmed := strings.TrimSpace(agentURL)
	if agent, exists := c.byName[trimmed]; exists {
		if agent == nil {
			return nil, fmt.Errorf("A2A agent name %q is ambiguous; please specify one of the agent URLs: %s", agentURL, c.availableAgentsSummary())
		}
		return agent.Client, nil
	}

	return nil, fmt.Errorf("unknown A2A agent %q; available agents: %s", agentURL, c.availableAgentsSummary())
}

func (c *Clients) availableAgentsSummary() string {
	parts := make([]string, 0, len(c.agents))
	for _, a := range c.agents {
		if a.Card != nil && a.Card.Name != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", a.URL, a.Card.Name))
		} else {
			parts = append(parts, a.URL)
		}
	}
	return strings.Join(parts, ", ")
}

func normalizeLookupKey(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func deduplicateURLs(urls []string) []string {
	seen := make(map[string]struct{}, len(urls))
	res := make([]string, 0, len(urls))
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		res = append(res, u)
	}
	return res
}

func newCardResolver(httpClient *http.Client) *agentcard.Resolver {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &agentcard.Resolver{
		Client:     httpClient,
		CardParser: compatCardParser(),
	}
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
				card.SupportedInterfaces = []*a2acore.AgentInterface{
					{
						URL:             legacy.URL,
						ProtocolBinding: a2acore.TransportProtocolJSONRPC,
						ProtocolVersion: ver,
					},
				}
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

	// Fallback to the v0.3 well-known path (/.well-known/agent.json) when given a base URL and
	// the default v1.0 path returned 404/405 or a non-card response (e.g. a catch-all "/" JSON-RPC handler).
	if isBaseURL(rawURL) && shouldFallbackToLegacyCardPath(err) {
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
	var syntaxErr *stdjson.SyntaxError
	return errors.Is(err, errInvalidAgentCard) || errors.As(err, &syntaxErr)
}

func isBaseURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Scheme != "file" && (u.Path == "" || u.Path == "/")
}

func newClientFactory(httpClient *http.Client) *a2aclient.Factory {
	return a2aclient.NewFactory(
		a2aclient.WithConfig(a2aclient.Config{
			PreferredTransports: []a2acore.TransportProtocol{
				a2acore.TransportProtocolJSONRPC,
				a2acore.TransportProtocolHTTPJSON,
				a2acore.TransportProtocolGRPC,
			},
		}),
		a2aclient.WithJSONRPCTransport(httpClient),
		a2aclient.WithRESTTransport(httpClient),
		a2av0.WithJSONRPCTransport(a2av0.JSONRPCTransportConfig{Client: httpClient}),
		a2av0.WithRESTTransport(a2av0.RESTTransportConfig{Client: httpClient}),
		a2aclient.WithTransport(a2acore.TransportProtocolGRPC, newGRPCTransportFactory(true)),
		a2aclient.WithCompatTransport(a2av0.Version, a2acore.TransportProtocolGRPC, newGRPCTransportFactory(false)),
	)
}

func newGRPCTransportFactory(v1 bool) a2aclient.TransportFactory {
	return a2aclient.TransportFactoryFn(func(ctx context.Context, card *a2acore.AgentCard, iface *a2acore.AgentInterface) (a2aclient.Transport, error) {
		target, creds := parseGRPCTarget(iface.URL)
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
		if err != nil {
			return nil, err
		}
		if v1 {
			return a2agrpcv1.NewGRPCTransport(conn), nil
		}
		return a2agrpcv0.NewGRPCTransport(conn), nil
	})
}

func parseGRPCTarget(rawURL string) (string, credentials.TransportCredentials) {
	u, err := url.Parse(rawURL)
	if err == nil && u.Host != "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "grpc":
			return u.Host, insecure.NewCredentials()
		case "https", "grpcs":
			return u.Host, credentials.NewTLS(&tls.Config{})
		}
	}

	host := rawURL
	if h, _, splitErr := net.SplitHostPort(rawURL); splitErr == nil {
		host = h
	}
	if host == "localhost" || isLoopbackIP(host) {
		return rawURL, insecure.NewCredentials()
	}
	return rawURL, credentials.NewTLS(&tls.Config{})
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
