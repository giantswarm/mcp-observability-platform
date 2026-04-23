package cmd

import "strings"

// secretArgKeys is the denylist of tool-argument names whose values must never
// appear in audit records. Matched case-insensitively against the full key
// and as a substring (so "api_key", "auth_token", "bearerToken" all hit).
// Additions are safe (redaction is a one-way projection); removals need a
// security review.
var secretArgKeys = []string{
	"token",
	"apikey", "api_key",
	"authorization",
	"cookie",
	"bearer",
	"password", "passwd",
	"secret",
	"credential", "credentials",
}

// redactSecretArgs replaces values of any key containing a secret-like
// substring with "[REDACTED]". Operates on the defensive copy audit.Logger
// gives us; safe to mutate in place.
func redactSecretArgs(args map[string]any) map[string]any {
	for k := range args {
		lk := strings.ToLower(k)
		for _, needle := range secretArgKeys {
			if strings.Contains(lk, needle) {
				args[k] = "[REDACTED]"
				break
			}
		}
	}
	return args
}
