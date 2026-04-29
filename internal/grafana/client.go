package grafana

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/giantswarm/mcp-observability-platform/internal/grafana")

// maxResponseBytes caps every upstream response read so a pathological
// body can't OOM the MCP pod. 16 MiB is well above any realistic
// query payload and far below pod RSS.
const maxResponseBytes = 16 << 20

// redactedHeader wraps a secret header value (auth token) so that %v / %#v
// prints of the Client never leak the credential. Use string(r) to get the
// raw value when setting the HTTP header.
type redactedHeader string

func (r redactedHeader) String() string   { return "[REDACTED]" }
func (r redactedHeader) GoString() string { return "[REDACTED]" }

// errInvalidDatasourceProxyPath is returned by DatasourceProxy when the
// caller supplies a path that could escape the
// /api/datasources/proxy/{id}/ prefix (SSRF defence).
var errInvalidDatasourceProxyPath = errors.New("grafana: invalid datasource proxy path")

// Config holds the connection parameters for Grafana. Exactly one of
// Token or BasicAuth must be set.
type Config struct {
	URL string
	// Token is a Grafana server-admin service-account token (preferred).
	Token string
	// BasicAuth is "user:password" for the built-in admin user. Used
	// when the Grafana version doesn't allow promoting SAs to Grafana
	// Server Admin via API. Mutually exclusive with Token.
	BasicAuth  string
	HTTPClient *http.Client
}

// User is Grafana's projection of one user account, as returned by
// /api/users/lookup.
type User struct {
	ID int64 `json:"id"`
}

// Client carries the Grafana operations not delegated to upstream's
// per-call GrafanaClient: server-admin lookups (which need OrgID=0 to
// use the SA's global context) and a generic datasource proxy.
type Client interface {
	VerifyServerAdmin(ctx context.Context) error
	LookupUser(ctx context.Context, loginOrEmail string) (*User, error)
	LookupDatasourceUIDByID(ctx context.Context, opts RequestOpts, id int64) (string, error)
	UserOrgs(ctx context.Context, userID int64) ([]UserOrgMembership, error)
	DatasourceProxy(ctx context.Context, opts RequestOpts, dsID int64, path string, query url.Values) (json.RawMessage, error)
}

type client struct {
	base       *url.URL
	authHeader redactedHeader
	http       *http.Client
}

// New validates cfg and returns a Client. Token and BasicAuth are
// mutually exclusive; one of them is required.
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
	hc := cfg.HTTPClient
	if hc == nil {
		base := &http.Transport{
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 16,
			// Cap concurrent connections per host so a tool-timeout
			// storm can't fan out unbounded sockets at one Grafana.
			MaxConnsPerHost: 32,
			IdleConnTimeout: 90 * time.Second,
		}
		// otelhttp.NewTransport emits a client span per request and
		// injects the W3C traceparent header so downstream Grafana
		// spans attach to our trace.
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
	return &client{base: u, authHeader: authHeader, http: hc}, nil
}

// RequestOpts controls org scoping and audit attribution on a single request.
// Caller is propagated to Grafana via X-Grafana-User so audit logs record
// the OIDC subject instead of the server-admin SA.
type RequestOpts struct {
	OrgID  int64
	Caller string // subject/email; forwarded as X-Grafana-User if non-empty
}

// UserOrgMembership is Grafana's projection of one org a user belongs to.
// Role is Grafana's computed role ("Admin" | "Editor" | "Viewer" | "None")
// evaluated against the SSO org_mapping setting.
type UserOrgMembership struct {
	OrgID int64  `json:"orgId"`
	Role  string `json:"role"`
}

// fetch is the sole HTTP entry point in this package. URL is built from
// c.base.JoinPath(path) locally — no caller can construct a *http.Request
// and hand it in, so the SA-token-bearing request cannot be directed
// off-origin from inside this package.
func (c *client) fetch(ctx context.Context, path string, query url.Values, opts RequestOpts) (status int, respBody []byte, contentType string, err error) {
	u := c.base.JoinPath(path)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}

	ctx, span := tracer.Start(ctx, "grafana."+strings.TrimPrefix(path, "/api/"),
		trace.WithAttributes(attribute.String("grafana.path", path)),
	)
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return 0, nil, "", fmt.Errorf("grafana: new request: %w", err)
	}
	req.Header.Set("Authorization", string(c.authHeader))
	req.Header.Set("Accept", "application/json")
	// OrgID==0 is the no-switch sentinel: server-admin calls and
	// /api/health run in the SA's global context, not a per-org one.
	if opts.OrgID > 0 {
		req.Header.Set("X-Grafana-Org-Id", strconv.FormatInt(opts.OrgID, 10))
	}
	if opts.Caller != "" {
		req.Header.Set("X-Grafana-User", opts.Caller)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return 0, nil, "", fmt.Errorf("grafana: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err = readLimited(resp)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return 0, nil, "", fmt.Errorf("grafana: GET %s: %w", path, err)
	}

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode >= 300 {
		span.SetStatus(codes.Error, fmt.Sprintf("http %d", resp.StatusCode))
	}
	return resp.StatusCode, respBody, resp.Header.Get("Content-Type"), nil
}

// fetchJSON wraps fetch for JSON endpoints: HTTP >= 300 becomes an error,
// Prometheus-family `{"status":"error"}` in a 200 body is translated
// into an error with the errorType/error message, and an explicitly
// non-JSON content-type (HTML from a misconfigured sidecar) surfaces
// as an error instead of a confusing parse failure downstream.
func (c *client) fetchJSON(ctx context.Context, path string, query url.Values, opts RequestOpts) (json.RawMessage, error) {
	status, respBody, contentType, err := c.fetch(ctx, path, query, opts)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("grafana: GET %s: status %d: %s", path, status, string(respBody))
	}
	if !isJSONContentType(contentType) {
		return nil, fmt.Errorf("grafana: GET %s: unexpected content-type %q (want application/json) — check that no sidecar is intercepting /api calls", path, contentType)
	}
	if err := detectPromError(respBody); err != nil {
		return nil, fmt.Errorf("grafana: GET %s: %w", path, err)
	}
	return json.RawMessage(respBody), nil
}

// isJSONContentType returns false only when a content-type is set AND
// recognisably not JSON. Missing/empty is permitted (no info → fall
// through to the JSON parse).
func isJSONContentType(ct string) bool {
	if ct == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// VerifyServerAdmin calls GET /api/orgs, which requires the server-admin
// role. 401/403 means the SA is not server-admin and cannot switch org
// via X-Grafana-Org-Id.
func (c *client) VerifyServerAdmin(ctx context.Context) error {
	status, body, _, err := c.fetch(ctx, "/api/orgs", nil, RequestOpts{})
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("grafana: SA is not server-admin (status %d)", status)
	}
	if status >= 300 {
		return fmt.Errorf("grafana: GET /api/orgs: status %d: %s", status, string(body))
	}
	return nil
}

// LookupUser resolves a caller identity (email or login) to a Grafana
// user. Returns (nil, nil) when the user doesn't exist yet — Grafana
// only provisions users on first login, so a never-seen caller is a
// valid state. Server-admin only.
func (c *client) LookupUser(ctx context.Context, loginOrEmail string) (*User, error) {
	if loginOrEmail == "" {
		return nil, errors.New("grafana: loginOrEmail is required")
	}
	q := url.Values{"loginOrEmail": []string{loginOrEmail}}
	status, body, _, err := c.fetch(ctx, "/api/users/lookup", q, RequestOpts{})
	if err != nil {
		return nil, fmt.Errorf("grafana: lookup %q: %w", loginOrEmail, err)
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status >= 300 {
		return nil, fmt.Errorf("grafana: lookup %q: status %d: %s", loginOrEmail, status, string(body))
	}
	var out User
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("grafana: lookup unmarshal: %w", err)
	}
	return &out, nil
}

// LookupDatasourceUIDByID fetches the datasource UID for a numeric ID
// in the given org. Needed because the GrafanaOrganization CR carries
// int64 IDs while upstream's GrafanaClient takes UIDs.
//
// TODO(uid-publish): once observability-operator publishes datasource
// UIDs in GrafanaOrganization.status.datasources[].uid, the Datasource
// type grows a UID field and this lookup goes away. Tracked in
// docs/roadmap.md.
func (c *client) LookupDatasourceUIDByID(ctx context.Context, opts RequestOpts, id int64) (string, error) {
	if id <= 0 {
		return "", errors.New("grafana: datasource id must be positive")
	}
	body, err := c.fetchJSON(ctx, fmt.Sprintf("/api/datasources/%d", id), nil, opts)
	if err != nil {
		return "", err
	}
	var out struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("grafana: datasource %d: %w", id, err)
	}
	if out.UID == "" {
		return "", fmt.Errorf("grafana: datasource %d has no uid", id)
	}
	return out.UID, nil
}

// UserOrgs returns the per-org roles Grafana has computed for the given
// user id (Admin/Editor/Viewer per org, as resolved from SSO org_mapping
// at the user's last login). Server-admin only.
func (c *client) UserOrgs(ctx context.Context, userID int64) ([]UserOrgMembership, error) {
	if userID <= 0 {
		return nil, errors.New("grafana: userID is required")
	}
	body, err := c.fetchJSON(ctx, fmt.Sprintf("/api/users/%d/orgs", userID), nil, RequestOpts{})
	if err != nil {
		return nil, err
	}
	var out []UserOrgMembership
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("grafana: user orgs unmarshal: %w", err)
	}
	return out, nil
}

// DatasourceProxy forwards a GET to /api/datasources/proxy/{dsID}/{path}
// in the given org. Grafana applies the datasource's provisioned tenant
// headers.
//
// path is caller-controlled (e.g. "api/v2/alerts", "api/search") and
// must be treated as untrusted; validateDatasourceProxyPath rejects
// anything that could escape the proxy prefix.
func (c *client) DatasourceProxy(ctx context.Context, opts RequestOpts, dsID int64, path string, query url.Values) (json.RawMessage, error) {
	if err := validateDatasourceProxyPath(path); err != nil {
		return nil, err
	}
	return c.fetchJSON(ctx, fmt.Sprintf("/api/datasources/proxy/%d/%s", dsID, path), query, opts)
}

// detectPromError scans the first few hundred bytes for a JSON object
// with {"status":"error"}; if present, returns an error carrying the
// message. Bounded-size scan so we don't regex-walk multi-MiB responses.
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

// validateDatasourceProxyPath is defence-in-depth against a future
// caller forgetting to url.PathEscape its input before passing the
// path to DatasourceProxy. Single-pass unescape.
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
	decoded, err := url.PathUnescape(p)
	if err != nil {
		return fmt.Errorf("%w: invalid URL escape: %v", errInvalidDatasourceProxyPath, err)
	}
	if strings.Contains(decoded, "..") {
		return fmt.Errorf("%w: contains dot-dot traversal", errInvalidDatasourceProxyPath)
	}
	return nil
}

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
