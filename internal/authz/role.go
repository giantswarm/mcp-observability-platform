package authz

import (
	"encoding/json"
	"strings"
)

// Role encodes a caller's permission level within a single Grafana org.
// Ordered so higher-privilege > lower-privilege numerically — see AtLeast
// for the intended comparison idiom.
type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleEditor
	RoleAdmin
)

func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleEditor:
		return "editor"
	case RoleAdmin:
		return "admin"
	default:
		return "none"
	}
}

// MarshalJSON serialises a Role as its lowercase string form so callers can
// embed Organization values directly into tool/resource payloads.
func (r Role) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// AtLeast reports whether r is at least as privileged as other. Prefer this
// over a direct `r < other` comparison — the iota ordering is invisible
// contract that would break on a reorder.
func (r Role) AtLeast(other Role) bool { return r >= other }

// roleFromGrafana converts Grafana's per-org role strings ("Admin",
// "Editor", "Viewer", "None" — see the API reference for
// /api/users/{id}/orgs) into our enum. Unknown values map to RoleNone
// so callers never get elevated by accident on a Grafana change.
//
// Note: Grafana's server-admin status ("Grafana Admin") is NOT a
// per-org role — it lives on /api/users/{id}.isGrafanaAdmin and is
// orthogonal to per-org role assignments. We deliberately do not honour it here:
// a server-admin SA may need full access to administer the system, but
// callers SHOULD have an explicit per-org role to act through this MCP.
func roleFromGrafana(s string) Role {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "admin":
		return RoleAdmin
	case "editor":
		return RoleEditor
	case "viewer":
		return RoleViewer
	default:
		return RoleNone
	}
}
