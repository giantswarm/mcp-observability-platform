package tools

import "testing"

func TestQualifyTempoTag(t *testing.T) {
	cases := []struct {
		scope, tag, want string
	}{
		{"", "", ""},
		{"span", "", ""},
		{"", "service.name", "service.name"},  // intrinsic scope = bare
		{"intrinsic", "duration", "duration"}, // intrinsic scope = bare
		{"span", "service.name", "span.service.name"},
		{"resource", "cluster", "resource.cluster"},
		{"event", "name", "event.name"},
		{"link", "target", "link.target"},
		{"RESOURCE", "case", "RESOURCE.case"},                            // case-insensitive match, original case preserved on output
		{"span", "intrinsic:already.scoped", "intrinsic:already.scoped"}, // `:` means already qualified → passthrough
		{"weird", "x", "weird.x"},                                        // unknown scope still qualifies (default arm)
	}
	for _, c := range cases {
		if got := qualifyTempoTag(c.scope, c.tag); got != c.want {
			t.Errorf("qualifyTempoTag(%q, %q) = %q, want %q", c.scope, c.tag, got, c.want)
		}
	}
}
