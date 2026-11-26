package a2aserver

import (
	"cmp"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	"github.com/speed2exe/ag/setting"
	"github.com/speed2exe/ag/tool"
	"github.com/speed2exe/ag/tool/skill"
)

// BuildAgentCards constructs the public and optional authenticated extended
// AgentCard from server settings, loaded SKILL.md definitions, tools, active
// transport endpoints, and optional JSON override files.
func BuildAgentCards(
	s *setting.Settings,
	skills *skill.Skills,
	tools []*tool.Tool,
	httpBaseURL string,
	grpcTarget string,
	auth AuthConfig,
) (publicCard *a2a.AgentCard, extendedCard *a2a.AgentCard, err error) {
	httpBaseURL = strings.TrimRight(strings.TrimSpace(httpBaseURL), "/")
	if httpBaseURL == "" {
		httpBaseURL = defaultHTTPURL(s.A2aServerAddr)
	}
	grpcTarget = strings.TrimSpace(grpcTarget)

	interfaces := buildInterfaces(httpBaseURL, grpcTarget, s.A2aServerCompatV0)
	cardSkills := skillsFromLoaded(skills)
	if len(cardSkills) == 0 {
		cardSkills = []a2a.AgentSkill{{
			ID:          "general",
			Name:        s.A2aServerName,
			Description: s.A2aServerDescription,
			Tags:        []string{"general", "assistant", "coding"},
		}}
	}

	var provider *a2a.AgentProvider
	if s.A2aServerProviderOrg != "" || s.A2aServerProviderURL != "" {
		provider = &a2a.AgentProvider{
			Org: s.A2aServerProviderOrg,
			URL: s.A2aServerProviderURL,
		}
	}

	hasExtended := auth.Enabled() || s.A2aServerExtendedCardPath != ""

	publicCard = &a2a.AgentCard{
		Name:                 s.A2aServerName,
		Description:          s.A2aServerDescription,
		Version:              s.A2aServerVersion,
		Provider:             provider,
		IconURL:              s.A2aServerIconURL,
		DocumentationURL:     s.A2aServerDocURL,
		SupportedInterfaces:  interfaces,
		DefaultInputModes:    slices.Clone(s.A2aServerInputModes),
		DefaultOutputModes:   slices.Clone(s.A2aServerOutputModes),
		Skills:               cardSkills,
		SecuritySchemes:      auth.SecuritySchemes(),
		SecurityRequirements: auth.SecurityRequirements(),
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
			ExtendedAgentCard: hasExtended,
		},
	}

	if s.A2aServerCardPath != "" {
		override, err := loadCardFile(s.A2aServerCardPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load agent card from %s: %w", s.A2aServerCardPath, err)
		}
		mergeCard(publicCard, override)
	}

	// Ensure capabilities faithfully reflect what the server enforces.
	publicCard.Capabilities.Streaming = true
	publicCard.Capabilities.PushNotifications = true
	publicCard.Capabilities.ExtendedAgentCard = hasExtended

	if hasExtended {
		extCopy, err := deepCopyA2A(publicCard)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to clone public agent card: %w", err)
		}
		if s.A2aServerExtendedCardPath != "" {
			extOverride, err := loadCardFile(s.A2aServerExtendedCardPath)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to load extended agent card from %s: %w", s.A2aServerExtendedCardPath, err)
			}
			mergeCard(extCopy, extOverride)
		} else if len(tools) > 0 {
			toolNames := make([]string, 0, len(tools))
			for _, t := range tools {
				toolNames = append(toolNames, t.Name)
			}
			slices.Sort(toolNames)
			extCopy.Skills = append(extCopy.Skills, a2a.AgentSkill{
				ID:          "agent_tools",
				Name:        "Agent Tools",
				Description: "Authenticated tool capabilities available to this agent: " + strings.Join(toolNames, ", "),
				Tags:        []string{"tools", "authenticated"},
			})
		}
		extCopy.Capabilities.Streaming = true
		extCopy.Capabilities.PushNotifications = true
		extCopy.Capabilities.ExtendedAgentCard = true
		extendedCard = extCopy
	}

	return publicCard, extendedCard, nil
}

func buildInterfaces(httpBaseURL, grpcTarget string, compatV0 bool) []*a2a.AgentInterface {
	var ifaces []*a2a.AgentInterface
	// A2A v1.0 interfaces first (preferred).
	ifaces = append(ifaces,
		a2a.NewAgentInterface(httpBaseURL+"/jsonrpc", a2a.TransportProtocolJSONRPC),
		a2a.NewAgentInterface(httpBaseURL, a2a.TransportProtocolHTTPJSON),
	)
	if grpcTarget != "" {
		ifaces = append(ifaces, a2a.NewAgentInterface(grpcTarget, a2a.TransportProtocolGRPC))
	}

	// A2A v0.3 compatibility interfaces.
	if compatV0 {
		ifaces = append(ifaces,
			&a2a.AgentInterface{
				URL:             httpBaseURL + "/v0/jsonrpc",
				ProtocolBinding: a2a.TransportProtocolJSONRPC,
				ProtocolVersion: a2av0.Version,
			},
			&a2a.AgentInterface{
				URL:             httpBaseURL,
				ProtocolBinding: a2a.TransportProtocolHTTPJSON,
				ProtocolVersion: a2av0.Version,
			},
		)
		if grpcTarget != "" {
			ifaces = append(ifaces, &a2a.AgentInterface{
				URL:             grpcTarget,
				ProtocolBinding: a2a.TransportProtocolGRPC,
				ProtocolVersion: a2av0.Version,
			})
		}
	}
	return ifaces
}

func defaultHTTPURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		if strings.HasPrefix(listenAddr, "http://") || strings.HasPrefix(listenAddr, "https://") {
			return strings.TrimRight(listenAddr, "/")
		}
		return "http://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, port))
}

func defaultGRPCURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return strings.TrimSpace(listenAddr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func loadCardFile(path string) (*a2a.AgentCard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parser := a2av0.NewAgentCardParser()
	return parser(data)
}

func mergeCard(dst, src *a2a.AgentCard) {
	if src == nil {
		return
	}
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.Version != "" {
		dst.Version = src.Version
	}
	if src.Provider != nil {
		dst.Provider = src.Provider
	}
	if src.IconURL != "" {
		dst.IconURL = src.IconURL
	}
	if src.DocumentationURL != "" {
		dst.DocumentationURL = src.DocumentationURL
	}
	if len(src.DefaultInputModes) > 0 {
		dst.DefaultInputModes = slices.Clone(src.DefaultInputModes)
	}
	if len(src.DefaultOutputModes) > 0 {
		dst.DefaultOutputModes = slices.Clone(src.DefaultOutputModes)
	}
	if len(src.Skills) > 0 {
		dst.Skills = slices.Clone(src.Skills)
	}
	if len(src.SupportedInterfaces) > 0 {
		dst.SupportedInterfaces = slices.Clone(src.SupportedInterfaces)
	}
	if len(src.SecuritySchemes) > 0 {
		dst.SecuritySchemes = maps.Clone(src.SecuritySchemes)
	}
	if len(src.SecurityRequirements) > 0 {
		dst.SecurityRequirements = slices.Clone(src.SecurityRequirements)
	}
	if len(src.Signatures) > 0 {
		dst.Signatures = slices.Clone(src.Signatures)
	}
	if len(src.Capabilities.Extensions) > 0 {
		dst.Capabilities.Extensions = slices.Clone(src.Capabilities.Extensions)
	}
}

func skillsFromLoaded(skills *skill.Skills) []a2a.AgentSkill {
	if skills == nil || len(skills.SkillByFilePath) == 0 {
		return nil
	}
	list := slices.Collect(maps.Values(skills.SkillByFilePath))
	slices.SortFunc(list, func(a, b *skill.Skill) int {
		return cmp.Compare(a.FilePath, b.FilePath)
	})

	usedIDs := make(map[string]int, len(list))
	out := make([]a2a.AgentSkill, 0, len(list))
	for _, sk := range list {
		fm, _ := sk.Frontmatter.(map[string]any)
		name := stringFromMap(fm, "name")
		if name == "" {
			name = filepath.Base(filepath.Dir(sk.FilePath))
		}
		desc := stringFromMap(fm, "description")
		if desc == "" {
			desc = fmt.Sprintf("Execute the %s skill", name)
		}

		id := slugify(name)
		if id == "" {
			id = "skill"
		}
		usedIDs[id]++
		if usedIDs[id] > 1 {
			id = fmt.Sprintf("%s-%d", id, usedIDs[id])
		}

		tags := stringSliceFromMap(fm, "tags")
		if cat := stringFromMap(fm, "category"); cat != "" && !slices.Contains(tags, cat) {
			tags = append(tags, cat)
		}
		if len(tags) == 0 {
			tags = []string{"skill"}
		}

		out = append(out, a2a.AgentSkill{
			ID:          id,
			Name:        name,
			Description: desc,
			Tags:        tags,
			Examples:    stringSliceFromMap(fm, "examples"),
		})
	}
	return out
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func stringSliceFromMap(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	var out []string
	switch list := v.(type) {
	case []string:
		for _, item := range list {
			if t := strings.TrimSpace(item); t != "" {
				out = append(out, t)
			}
		}
	case []any:
		for _, item := range list {
			if s, ok := item.(string); ok {
				if t := strings.TrimSpace(s); t != "" {
					out = append(out, t)
				}
			}
		}
	}
	return out
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
