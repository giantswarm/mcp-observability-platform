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

// String returns the lowercase canonical name.
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
// embed OrgAccess values directly into tool/resource payloads.
func (r Role) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// AtLeast reports whether r is at least as privileged as other. Prefer this
// over a direct `r < other` comparison — the iota ordering is invisible
// contract that would break on a reorder.
func (r Role) AtLeast(other Role) bool { return r >= other }

// roleFromGrafana converts Grafana's role strings ("Admin", "Editor",
// "Viewer", "None") into our enum. Unknown values map to RoleNone so
// callers never get elevated by accident on a Grafana change.
func roleFromGrafana(s string) Role {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "admin", "grafana admin":
		return RoleAdmin
	case "editor":
		return RoleEditor
	case "viewer":
		return RoleViewer
	default:
		return RoleNone
	}
}
