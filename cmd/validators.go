package cmd

import "strings"

// splitAndTrimCSV splits a comma-separated env var value, trims whitespace
// around each entry, and drops empty results. "" → nil (not []string{""}).
func splitAndTrimCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
