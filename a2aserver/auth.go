package a2aserver

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const (
	defaultAPIKeyHeader  = "X-API-Key"
	schemeNameBearerAuth = a2a.SecuritySchemeName("bearerAuth")
	schemeNameAPIKeyAuth = a2a.SecuritySchemeName("apiKeyAuth")
	authUserNameBearer   = "bearer"
	authUserNameAPIKey   = "apikey"
)

// AuthConfig configures optional authentication for the A2A server.
type AuthConfig struct {
	BearerToken  string
	APIKey       string
	APIKeyHeader string
}

// Enabled reports whether at least one authentication mechanism is configured.
func (c AuthConfig) Enabled() bool {
	return c.BearerToken != "" || c.APIKey != ""
}

func (c AuthConfig) apiKeyHeader() string {
	if h := strings.TrimSpace(c.APIKeyHeader); h != "" {
		return h
	}
	return defaultAPIKeyHeader
}

// SecuritySchemes returns the A2A SecuritySchemes declarations matching c.
func (c AuthConfig) SecuritySchemes() a2a.NamedSecuritySchemes {
	if !c.Enabled() {
		return nil
	}
	schemes := make(a2a.NamedSecuritySchemes)
	if c.BearerToken != "" {
		schemes[schemeNameBearerAuth] = a2a.HTTPAuthSecurityScheme{
			Scheme:      "bearer",
			Description: "HTTP Bearer token authentication via the Authorization header.",
		}
	}
	if c.APIKey != "" {
		schemes[schemeNameAPIKeyAuth] = a2a.APIKeySecurityScheme{
			Location:    a2a.APIKeySecuritySchemeLocationHeader,
			Name:        c.apiKeyHeader(),
			Description: "API key authentication via HTTP header or gRPC metadata.",
		}
	}
	return schemes
}

// SecurityRequirements returns the A2A SecurityRequirementsOptions matching c.
// When both Bearer token and API key are configured, either satisfies the requirement.
func (c AuthConfig) SecurityRequirements() a2a.SecurityRequirementsOptions {
	if !c.Enabled() {
		return nil
	}
	var reqs a2a.SecurityRequirementsOptions
	if c.BearerToken != "" {
		reqs = append(reqs, a2a.SecurityRequirements{
			schemeNameBearerAuth: a2a.SecuritySchemeScopes{},
		})
	}
	if c.APIKey != "" {
		reqs = append(reqs, a2a.SecurityRequirements{
			schemeNameAPIKeyAuth: a2a.SecuritySchemeScopes{},
		})
	}
	return reqs
}

// AuthInterceptor enforces configured Bearer token and/or API key authentication
// across all A2A transports (JSON-RPC, HTTP+JSON REST, and gRPC) and populates
// CallContext.User on success.
type AuthInterceptor struct {
	a2asrv.PassthroughCallInterceptor
	cfg AuthConfig
}

// NewAuthInterceptor creates an AuthInterceptor for cfg.
func NewAuthInterceptor(cfg AuthConfig) *AuthInterceptor {
	return &AuthInterceptor{cfg: cfg}
}

// Before implements a2asrv.CallInterceptor.
func (a *AuthInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	if !a.cfg.Enabled() {
		return ctx, nil, nil
	}
	if callCtx == nil || callCtx.ServiceParams() == nil {
		return ctx, nil, a2a.ErrUnauthenticated
	}

	params := callCtx.ServiceParams()

	if a.cfg.BearerToken != "" {
		if vals, ok := params.Get("authorization"); ok {
			for _, raw := range vals {
				if token, ok := parseBearerToken(raw); ok && constantTimeEqual(token, a.cfg.BearerToken) {
					callCtx.User = a2asrv.NewAuthenticatedUser(authUserNameBearer, map[string]any{
						"scheme": string(schemeNameBearerAuth),
					})
					return ctx, nil, nil
				}
			}
		}
	}

	if a.cfg.APIKey != "" {
		if vals, ok := params.Get(a.cfg.apiKeyHeader()); ok {
			for _, raw := range vals {
				if constantTimeEqual(strings.TrimSpace(raw), a.cfg.APIKey) {
					callCtx.User = a2asrv.NewAuthenticatedUser(authUserNameAPIKey, map[string]any{
						"scheme": string(schemeNameAPIKeyAuth),
					})
					return ctx, nil, nil
				}
			}
		}
	}

	return ctx, nil, a2a.ErrUnauthenticated
}

func parseBearerToken(headerVal string) (string, bool) {
	trimmed := strings.TrimSpace(headerVal)
	if len(trimmed) < len("Bearer ") {
		return "", false
	}
	if !strings.EqualFold(trimmed[:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(trimmed[len("Bearer "):])
	if token == "" {
		return "", false
	}
	return token, true
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
