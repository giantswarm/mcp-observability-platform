// Package grafana is a thin HTTP client for the Grafana API used by this MCP.
//
// It assumes the caller provides a Grafana server-admin service-account token
// (an SA granted the "Grafana Admin" server role), so that X-Grafana-Org-Id
// can switch org context per request. Regular org-scoped SAs will NOT work.
package grafana

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/grafana")

// maxResponseBytes caps every upstream response read so a pathological body
// (huge rendered panel, runaway JSON) cannot OOM the MCP pod. 16 MiB is well
// above any realistic dashboard / query payload and still far below pod RSS.
const maxResponseBytes = 16 << 20

// redactedHeader wraps a secret header value (auth token) so that %v / %#v
// prints of the Client never leak the credential. Use string(r) to get the
// raw value when setting the HTTP header.
type redactedHeader string

func (r redactedHeader) String() string   { return "[REDACTED]" }
func (r redactedHeader) GoString() string { return "[REDACTED]" }

// errInvalidDatasourceProxyPath is returned by DatasourceProxy when the caller
// supplies a path that could escape the /api/datasources/proxy/{id}/ prefix
// (SSRF defence).
var errInvalidDatasourceProxyPath = errors.New("grafana: invalid datasource proxy path")

// Config holds the connection parameters for Grafana. Exactly one of Token or
// BasicAuth must be set.
type Config struct {
	// URL is the in-cluster/admin-facing Grafana URL used for every API call.
	URL string
	// PublicURL is the human-facing Grafana URL used to build deeplinks
	// handed back to operators (e.g. via generate_deeplink). Optional —
	// defaults to URL when empty, which is usually wrong if URL is the
	// internal Service DNS.
	PublicURL string
	// Token is a Grafana server-admin service-account token (preferred).
	Token string
	// BasicAuth is "user:password" for the built-in admin user. Used when the
	// Grafana version doesn't allow promoting SAs to Grafana Server Admin via
	// API. Mutually exclusive with Token.
	BasicAuth  string
	HTTPClient *http.Client
}

// Client is the consumer-facing port onto Grafana. The concrete
// implementation (the unexported `client` struct below) is built by New;
// tests pass a fake implementing this interface.
type Client interface {
	Ping(ctx context.Context) error
	VerifyServerAdmin(ctx context.Context) error
	BaseURL() (*url.URL, error)
	HasImageRenderer(ctx context.Context) (bool, error)
	LookupUser(ctx context.Context, loginOrEmail string) (*struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Login string `json:"login"`
	}, error)
	UserOrgs(ctx context.Context, userID int64) ([]UserOrgMembership, error)
	GetDashboard(ctx context.Context, opts RequestOpts, uid string) (json.RawMessage, error)
	SearchDashboards(ctx context.Context, opts RequestOpts, query string, limit int) (json.RawMessage, error)
	SearchFolders(ctx context.Context, opts RequestOpts, query string, limit int) (json.RawMessage, error)
	ListDatasources(ctx context.Context, opts RequestOpts) (json.RawMessage, error)
	GetDatasource(ctx context.Context, opts RequestOpts, uid string) (json.RawMessage, error)
	GetAnnotations(ctx context.Context, opts RequestOpts, q url.Values) (json.RawMessage, error)
	GetAnnotationTags(ctx context.Context, opts RequestOpts, q url.Values) (json.RawMessage, error)
	DatasourceProxy(ctx context.Context, opts RequestOpts, dsID int64, path string, query url.Values) (json.RawMessage, error)
	RenderPanel(ctx context.Context, opts RequestOpts, dashboardUID string, panelID int, q url.Values) ([]byte, string, error)
}

// client is the concrete Grafana HTTP client.
type client struct {
	base       *url.URL
	publicBase *url.URL
	authHeader redactedHeader
	http       *http.Client

	// In-process cache for HasImageRenderer; TTL 5 minutes. The plugin is
	// almost never installed/uninstalled at runtime, so this tiny cache
	// saves a round-trip on every get_panel_image call.
	rendererMu      sync.RWMutex
	rendererAt      time.Time
	rendererPresent bool
	rendererErr     error
}

// New constructs a Client. Returns an error if URL is empty/unparseable or
// neither/both credentials are set.
func New(cfg Config) (Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("grafana: URL is required")
	}
	if cfg.Token == "" && cfg.BasicAuth == "" {
		return nil, errors.New("grafana: Token or BasicAuth is required")
	}
	if cfg.Token != "" && cfg.BasicAuth != "" {
		return nil, errors.New("grafana: set only one of Token or BasicAuth")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("grafana: parse URL: %w", err)
	}
	publicBase := u
	if cfg.PublicURL != "" {
		pu, err := url.Parse(cfg.PublicURL)
		if err != nil {
			return nil, fmt.Errorf("grafana: parse PublicURL: %w", err)
		}
		publicBase = pu
	}
	hc := cfg.HTTPClient
	if hc == nil {
		base := &http.Transport{
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		}
		// otelhttp.NewTransport emits a client span per request and injects the
		// W3C traceparent header so downstream Grafana spans attach to our trace.
		hc = &http.Client{
			Timeout: 30 * time.Second,
			Transport: otelhttp.NewTransport(base,
				otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
					return "grafana " + r.Method + " " + r.URL.Path
				}),
			),
		}
	}
	authHeader := redactedHeader("Bearer " + cfg.Token)
	if cfg.BasicAuth != "" {
		authHeader = redactedHeader("Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.BasicAuth)))
	}
	return &client{base: u, publicBase: publicBase, authHeader: authHeader, http: hc}, nil
}

// RequestOpts controls org scoping and audit attribution on a single request.
// Caller is propagated to Grafana via X-Grafana-User so audit logs record the
// OIDC subject instead of the server-admin SA.
type RequestOpts struct {
	OrgID  int64
	Caller string // subject/email; forwarded as X-Grafana-User if non-empty
}

// Ping calls GET /api/health, Grafana's auth-free reachability endpoint.
// Used by readiness probes; cheaper than VerifyServerAdmin (which lists all
// orgs). Returns nil on 2xx.
func (c *client) Ping(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/health", nil, nil, RequestOpts{})
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("grafana: GET /api/health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("grafana: GET /api/health: status %d", resp.StatusCode)
	}
	return nil
}

// VerifyServerAdmin calls GET /api/orgs, which requires the server-admin role.
// 401/403 means the SA is not server-admin and cannot switch org via X-Grafana-Org-Id.
func (c *client) VerifyServerAdmin(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/orgs", nil, nil, RequestOpts{})
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("grafana: GET /api/orgs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("grafana: SA is not server-admin (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		body, rerr := readLimited(resp)
		if rerr != nil {
			return fmt.Errorf("grafana: GET /api/orgs: status %d: %w", resp.StatusCode, rerr)
		}
		return fmt.Errorf("grafana: GET /api/orgs: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// GetDashboard fetches a dashboard by UID, in the given Grafana org.
func (c *client) GetDashboard(ctx context.Context, opts RequestOpts, uid string) (json.RawMessage, error) {
	if uid == "" {
		return nil, errors.New("grafana: dashboard uid is required")
	}
	return c.doGET(ctx, "/api/dashboards/uid/"+url.PathEscape(uid), nil, opts)
}

// SearchDashboards returns dashboards visible in the given org. Results are
// bounded by limit (defaulting to 100); Grafana's API caps this at 5000.
func (c *client) SearchDashboards(ctx context.Context, opts RequestOpts, query string, limit int) (json.RawMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{"type": []string{"dash-db"}, "limit": []string{strconv.Itoa(limit)}}
	if query != "" {
		q.Set("query", query)
	}
	return c.doGET(ctx, "/api/search", q, opts)
}

// SearchFolders returns folders visible in the given org. Same endpoint as
// SearchDashboards but with type=dash-folder.
func (c *client) SearchFolders(ctx context.Context, opts RequestOpts, query string, limit int) (json.RawMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{"type": []string{"dash-folder"}, "limit": []string{strconv.Itoa(limit)}}
	if query != "" {
		q.Set("query", query)
	}
	return c.doGET(ctx, "/api/search", q, opts)
}

// ListDatasources returns the datasources visible in the given org.
func (c *client) ListDatasources(ctx context.Context, opts RequestOpts) (json.RawMessage, error) {
	return c.doGET(ctx, "/api/datasources", nil, opts)
}

// GetDatasource returns full datasource details by UID.
func (c *client) GetDatasource(ctx context.Context, opts RequestOpts, uid string) (json.RawMessage, error) {
	if uid == "" {
		return nil, errors.New("grafana: datasource uid is required")
	}
	return c.doGET(ctx, "/api/datasources/uid/"+url.PathEscape(uid), nil, opts)
}

// GetAnnotations forwards a query to /api/annotations. Caller assembles q.
func (c *client) GetAnnotations(ctx context.Context, opts RequestOpts, q url.Values) (json.RawMessage, error) {
	return c.doGET(ctx, "/api/annotations", q, opts)
}

// GetAnnotationTags returns the set of tags used across annotations in the
// given org, optionally filtered by a name prefix. Matches upstream
// grafana/mcp-grafana's get_annotation_tags.
func (c *client) GetAnnotationTags(ctx context.Context, opts RequestOpts, q url.Values) (json.RawMessage, error) {
	return c.doGET(ctx, "/api/annotations/tags", q, opts)
}

// UserOrgMembership is Grafana's projection of one org a user belongs to.
// Role is Grafana's computed role ("Admin" | "Editor" | "Viewer" | "None")
// evaluated against the SSO org_mapping setting.
type UserOrgMembership struct {
	OrgID   int64  `json:"orgId"`
	OrgName string `json:"name"`
	Role    string `json:"role"`
}

// LookupUser resolves a caller identity (email or login) to a Grafana user.
// Returns (nil, nil) with no error when the user doesn't exist yet — Grafana
// only provisions users on first login, so a never-seen caller is a valid
// state. Needs a server-admin credential.
func (c *client) LookupUser(ctx context.Context, loginOrEmail string) (*struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Login string `json:"login"`
}, error) {
	if loginOrEmail == "" {
		return nil, errors.New("grafana: loginOrEmail is required")
	}
	q := url.Values{"loginOrEmail": []string{loginOrEmail}}
	req, err := c.newRequest(ctx, http.MethodGet, "/api/users/lookup", q, nil, RequestOpts{})
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grafana: lookup %q: %w", loginOrEmail, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := readLimited(resp)
	if err != nil {
		return nil, fmt.Errorf("grafana: lookup %q: %w", loginOrEmail, err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("grafana: lookup %q: status %d: %s", loginOrEmail, resp.StatusCode, string(body))
	}
	var out struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("grafana: lookup unmarshal: %w", err)
	}
	return &out, nil
}

// UserOrgs returns the org memberships Grafana has computed for the given
// user id (Admin/Editor/Viewer per org, as resolved from SSO org_mapping at
// the user's last login). Server-admin only.
func (c *client) UserOrgs(ctx context.Context, userID int64) ([]UserOrgMembership, error) {
	if userID <= 0 {
		return nil, errors.New("grafana: userID is required")
	}
	body, err := c.doGET(ctx, fmt.Sprintf("/api/users/%d/orgs", userID), nil, RequestOpts{})
	if err != nil {
		return nil, err
	}
	var out []UserOrgMembership
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("grafana: user orgs unmarshal: %w", err)
	}
	return out, nil
}

// BaseURL returns a defensive copy of the user-facing Grafana URL (PublicURL
// if set, otherwise the admin URL). Callers use it to build deeplinks handed
// back to human operators, NOT for API traffic.
func (c *client) BaseURL() (*url.URL, error) {
	u, err := url.Parse(c.publicBase.String())
	if err != nil {
		return nil, fmt.Errorf("grafana: parse public base url: %w", err)
	}
	return u, nil
}

// HasImageRenderer probes Grafana for the grafana-image-renderer plugin.
// Returns true only when the plugin is installed AND reachable. The result
// is cached in-process for 5 minutes so repeated get_panel_image calls do
// not ping /api/plugins on every request.
func (c *client) HasImageRenderer(ctx context.Context) (bool, error) {
	c.rendererMu.RLock()
	if time.Since(c.rendererAt) < 5*time.Minute {
		present, err := c.rendererPresent, c.rendererErr
		c.rendererMu.RUnlock()
		return present, err
	}
	c.rendererMu.RUnlock()

	c.rendererMu.Lock()
	defer c.rendererMu.Unlock()
	// Double-check inside the write lock in case another goroutine just refreshed.
	if time.Since(c.rendererAt) < 5*time.Minute {
		return c.rendererPresent, c.rendererErr
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/api/plugins/grafana-image-renderer/settings", nil, nil, RequestOpts{})
	if err != nil {
		c.rendererErr = err
		c.rendererAt = time.Now()
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.rendererErr = err
		c.rendererAt = time.Now()
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	// 200 -> plugin settings exist -> installed; 404 -> not installed.
	// Anything else (403 etc.) treat as unknown; retry later.
	c.rendererAt = time.Now()
	c.rendererErr = nil
	c.rendererPresent = resp.StatusCode == http.StatusOK
	return c.rendererPresent, nil
}

// DatasourceProxy forwards a GET to /api/datasources/proxy/{dsID}/{path} in the
// given org. Grafana applies the datasource's provisioned tenant headers.
//
// path is caller-controlled (tool handlers build it from user input such as
// "api/v1/query" or "loki/api/v1/query_range"). validateDatasourceProxyPath
// rejects anything that could escape the proxy prefix.
func (c *client) DatasourceProxy(ctx context.Context, opts RequestOpts, dsID int64, path string, query url.Values) (json.RawMessage, error) {
	if err := validateDatasourceProxyPath(path); err != nil {
		return nil, err
	}
	return c.doGET(ctx, fmt.Sprintf("/api/datasources/proxy/%d/%s", dsID, path), query, opts)
}

// RenderPanel fetches a rendered panel image from Grafana's render endpoint.
// Returns the raw PNG bytes plus the content type. Requires the
// grafana-image-renderer plugin or a renderer sidecar configured via
// GF_RENDERING_SERVER_URL; without it Grafana returns an HTML error page.
// The returned error includes a pointer to the setup docs when the renderer
// is not available.
func (c *client) RenderPanel(ctx context.Context, opts RequestOpts, dashboardUID string, panelID int, q url.Values) ([]byte, string, error) {
	if dashboardUID == "" {
		return nil, "", errors.New("grafana: dashboard uid is required")
	}
	if panelID <= 0 {
		return nil, "", errors.New("grafana: panelId is required and must be > 0")
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("panelId", strconv.Itoa(panelID))
	req, err := c.newRequest(ctx, http.MethodGet, "/render/d-solo/"+url.PathEscape(dashboardUID), q, nil, opts)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/png")
	ctx, span := tracer.Start(ctx, "grafana.render_panel",
		trace.WithAttributes(attribute.Int64("grafana.org_id", opts.OrgID), attribute.String("grafana.dashboard_uid", dashboardUID), attribute.Int("grafana.panel_id", panelID)),
	)
	defer span.End()
	req = req.WithContext(ctx)
	resp, err := c.http.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, "", fmt.Errorf("grafana: render: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readLimited(resp)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, "", fmt.Errorf("grafana: render: %w", err)
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 300 {
		span.SetStatus(codes.Error, fmt.Sprintf("http %d", resp.StatusCode))
		if strings.Contains(ct, "text/html") || strings.Contains(string(body), "Rendering") {
			return nil, "", fmt.Errorf(
				"grafana: image renderer not available (status %d). Install the 'grafana-image-renderer' plugin in Grafana, or deploy the renderer as a sidecar/Deployment and set GF_RENDERING_SERVER_URL + GF_RENDERING_CALLBACK_URL on Grafana. See https://grafana.com/grafana/plugins/grafana-image-renderer/",
				resp.StatusCode)
		}
		return nil, "", fmt.Errorf("grafana: render: status %d: %s", resp.StatusCode, string(body))
	}
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", fmt.Errorf(
			"grafana: render returned non-image content-type %q — check that the image renderer is installed and reachable",
			ct)
	}
	return body, ct, nil
}

func (c *client) doGET(ctx context.Context, path string, query url.Values, opts RequestOpts) (json.RawMessage, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, query, nil, opts)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, req, path)
}

func (c *client) do(ctx context.Context, req *http.Request, path string) (json.RawMessage, error) {
	ctx, span := tracer.Start(ctx, "grafana."+strings.TrimPrefix(path, "/api/"),
		trace.WithAttributes(attribute.String("http.method", req.Method), attribute.String("grafana.path", path)),
	)
	defer span.End()
	req = req.WithContext(ctx)
	resp, err := c.http.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("grafana: %s %s: %w", req.Method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readLimited(resp)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("grafana: %s %s: %w", req.Method, path, err)
	}
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode >= 300 {
		span.SetStatus(codes.Error, fmt.Sprintf("http %d", resp.StatusCode))
		return nil, fmt.Errorf("grafana: %s %s: status %d: %s", req.Method, path, resp.StatusCode, string(body))
	}
	// Prometheus-family datasources (Mimir, Loki /labels, /series) return
	// 200 with `{"status":"error", "error":"..."}` when the query is malformed.
	// Detect that here so the MCP surface treats it as a real error rather
	// than returning a success-shaped error payload.
	if err := detectPromError(body); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("grafana: %s %s: %w", req.Method, path, err)
	}
	return json.RawMessage(body), nil
}

// detectPromError scans the first few hundred bytes for a JSON object with
// {"status":"error"}; if present, returns an error carrying the message.
// Bounded-size scan so we don't regex-walk multi-MiB responses.
func detectPromError(body []byte) error {
	if len(body) == 0 || body[0] != '{' {
		return nil
	}
	head := body
	if len(head) > 1024 {
		head = head[:1024]
	}
	if !bytes.Contains(head, []byte(`"status":"error"`)) &&
		!bytes.Contains(head, []byte(`"status": "error"`)) {
		return nil
	}
	var peek struct {
		Status    string `json:"status"`
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return nil
	}
	if peek.Status != "error" {
		return nil
	}
	if peek.ErrorType != "" {
		return fmt.Errorf("upstream error (%s): %s", peek.ErrorType, peek.Error)
	}
	return fmt.Errorf("upstream error: %s", peek.Error)
}

// newRequest constructs a request with Bearer auth and, when orgID > 0,
// X-Grafana-Org-Id set so that Grafana scopes the call to that org.
// orgID == 0 means "use Grafana's default org context" (only safe for
// server-level endpoints such as /api/orgs).
func (c *client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader, opts RequestOpts) (*http.Request, error) {
	u := c.base.JoinPath(path)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("grafana: new request: %w", err)
	}
	req.Header.Set("Authorization", string(c.authHeader))
	req.Header.Set("Accept", "application/json")
	if opts.OrgID > 0 {
		req.Header.Set("X-Grafana-Org-Id", strconv.FormatInt(opts.OrgID, 10))
	}
	// Forwarded to Grafana's audit log so server-admin SA calls are
	// attributed back to the MCP caller's OIDC subject/email. Sanitise
	// first (strip control chars, cap length) to block header injection.
	if caller := sanitizeCallerHeader(opts.Caller); caller != "" {
		req.Header.Set("X-Grafana-User", caller)
	}
	return req, nil
}

// validateDatasourceProxyPath is a minimal guard against future callers that
// forget to url.PathEscape user input. Every current caller either passes a
// string literal or an escaped value, so this is defence-in-depth, not a
// live SSRF patch. Grafana's datasource proxy itself only reaches the
// configured datasource URL — a traversal would at worst reach a different
// read-only endpoint on that same datasource.
func validateDatasourceProxyPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty", errInvalidDatasourceProxyPath)
	}
	if len(p) > 1024 {
		return fmt.Errorf("%w: too long (%d bytes)", errInvalidDatasourceProxyPath, len(p))
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: leading slash", errInvalidDatasourceProxyPath)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("%w: contains dot-dot traversal", errInvalidDatasourceProxyPath)
	}
	return nil
}

// readLimited caps per-response body reads at maxResponseBytes. Unbounded
// io.ReadAll on the image-renderer endpoint (user-controlled width/height)
// is the main OOM vector; the other call sites get the same treatment for
// consistency.
func readLimited(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("grafana: read body: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("grafana: response exceeded %d bytes", maxResponseBytes)
	}
	return body, nil
}

// sanitizeCallerHeader strips control characters and non-printable-ASCII from
// the caller identity before it hits X-Grafana-User. Prevents header
// injection (CRLF smuggling) and caps length at 256 bytes. Returns "" when
// the input is empty or has no printable bytes.
func sanitizeCallerHeader(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Printable ASCII only. Drops CR, LF, TAB, NUL, DEL and everything
		// non-ASCII. OIDC subjects / emails are ASCII in practice.
		if c < 0x20 || c > 0x7e {
			continue
		}
		b.WriteByte(c)
		if b.Len() >= 256 {
			break
		}
	}
	return b.String()
}
