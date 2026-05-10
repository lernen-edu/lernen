// Package context7 implements docs.Provider against Context7's REST API
// at https://context7.com/api/v2.
//
// API shape verified via Context7 itself (per BUILD_ORDER M2):
//   - GET /api/v2/libs/search?libraryName=<name>&query=<q> →
//       {results: [{id, title, ...}]} — used for ResolveLibrary
//   - GET /api/v2/context?libraryId=<id>&query=<topic> →
//       markdown text by default — used for QueryDocs (the
//       Provider contract requires "no JSON envelopes, no chrome",
//       so we use the plain-markdown response, not type=json)
//   - Authorization: Bearer <CONTEXT7_API_KEY>
//
// The MCP-over-HTTP endpoint at https://mcp.context7.com/mcp is a
// different protocol (MCP). Lernen uses REST because the abstraction
// is internal/docs.Provider, not the wire format; if MCP-everywhere
// becomes worth the lift later, swap implementations behind the
// interface.
//
// Boundary discipline:
//   - API key resolved at construction from cfg.APIKeyEnv. Never logged,
//     never echoed in errors. If the response body is ever surfaced
//     up, it gets surfaced via slog at debug level — never in the
//     user-facing error string, since Context7 may echo request
//     metadata that includes our headers.
//   - Every QueryDocs body passes through docs.Sanitize before return.
//     Frame is NOT applied here — that is the system-prompt builder's
//     job, per the Provider contract in internal/docs/provider.go:46-49.
package context7

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/docs"
)

const (
	defaultBaseURL = "https://context7.com"
	defaultUA      = "lernen-docs-context7/0.1"
	requestTimeout = 30 * time.Second
)

var _ docs.Provider = (*Provider)(nil)

// Config carries the Context7-specific settings. Decoupled from
// internal/config so providers do not learn about TOML.
type Config struct {
	// APIKeyEnv names the environment variable holding the API key.
	APIKeyEnv string
}

// Provider is the Context7 REST docs provider.
type Provider struct {
	cfg       Config
	apiKey    string
	baseURL   string
	userAgent string
	client    *http.Client
}

// Option configures a Provider at construction time.
type Option func(*Provider)

// WithBaseURL overrides the Context7 base URL. For tests against
// httptest.NewServer.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the http.Client. For tests with custom
// transports or shorter timeouts.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(p *Provider) { p.userAgent = ua }
}

// New constructs a Provider. The API key is resolved at construction
// time from the environment variable named in cfg.APIKeyEnv. An empty
// resolved value is not an error here — ResolveLibrary, QueryDocs, and
// HealthCheck all surface docs.ErrNotConfigured when called, which the
// caller can route to `lernen setup`.
func New(cfg Config, opts ...Option) *Provider {
	p := &Provider{
		cfg:       cfg,
		apiKey:    os.Getenv(cfg.APIKeyEnv),
		baseURL:   defaultBaseURL,
		userAgent: defaultUA,
		client:    &http.Client{Timeout: requestTimeout},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ResolveLibrary maps a human library name to a Context7 library ID by
// calling /api/v2/libs/search and returning the first result's ID.
func (p *Provider) ResolveLibrary(ctx context.Context, name string) (docs.LibraryID, error) {
	if p.apiKey == "" {
		return "", p.missingKeyErr()
	}

	q := url.Values{}
	q.Set("libraryName", name)
	q.Set("query", name)
	endpoint := p.baseURL + "/api/v2/libs/search?" + q.Encode()

	body, err := p.doGET(ctx, endpoint)
	if err != nil {
		return "", err
	}

	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("context7: decode search response: %w", err)
	}
	if len(resp.Results) == 0 {
		return "", fmt.Errorf("context7: no results for %q", name)
	}
	return docs.LibraryID(resp.Results[0].ID), nil
}

// QueryDocs returns sanitized markdown for a topic within lib. The
// caller must not embed the result in a system prompt without first
// passing it through docs.Frame, which applies the data-not-instructions
// envelope (see THREAT_MODEL §5).
func (p *Provider) QueryDocs(ctx context.Context, lib docs.LibraryID, topic string, _ int) (string, error) {
	if p.apiKey == "" {
		return "", p.missingKeyErr()
	}

	q := url.Values{}
	q.Set("libraryId", string(lib))
	q.Set("query", topic)
	endpoint := p.baseURL + "/api/v2/context?" + q.Encode()

	body, err := p.doGET(ctx, endpoint)
	if err != nil {
		return "", err
	}

	sanitized, _ := docs.Sanitize(string(body), docs.SanitizeOptions{})
	return sanitized, nil
}

// HealthCheck issues a tiny libs/search request to verify auth and
// network reach in one shot. Surfaces docs.ErrNotConfigured for an
// unset key, an env-var-named hint for 401, and a wrapped status code
// for other failures.
func (p *Provider) HealthCheck(ctx context.Context) error {
	if p.apiKey == "" {
		return p.missingKeyErr()
	}

	q := url.Values{}
	q.Set("libraryName", "lernen-healthcheck")
	q.Set("query", "ping")
	endpoint := p.baseURL + "/api/v2/libs/search?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("context7: build request: %w", err)
	}
	p.setStandardHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("context7: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("context7: 401 Unauthorized — check the %s environment variable", p.cfg.APIKeyEnv)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("context7: health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (p *Provider) doGET(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("context7: build request: %w", err)
	}
	p.setStandardHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("context7: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("context7: 401 Unauthorized — check the %s environment variable", p.cfg.APIKeyEnv)
	}
	if resp.StatusCode >= 400 {
		// Don't echo the response body — Context7 may include request
		// metadata back to the caller, and we don't want to surface
		// anything that could carry a header value or query string.
		return nil, fmt.Errorf("context7: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("context7: read response: %w", err)
	}
	return body, nil
}

func (p *Provider) setStandardHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("User-Agent", p.userAgent)
}

func (p *Provider) missingKeyErr() error {
	return fmt.Errorf("context7: %s environment variable is not set; configure it or run `lernen setup`: %w", p.cfg.APIKeyEnv, docs.ErrNotConfigured)
}

// ---- wire types ----

type searchResponse struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}
