// Package tools — pagination.go: shared pagination envelope for list_* tools that return flat string slices.
package tools

import (
	"slices"
	"sort"
	"strings"
)

// paginatedStrings is the JSON projection used by every "list_*" tool that
// returns a flat list of strings (metric names, label values, tag values…).
type paginatedStrings struct {
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
	HasMore  bool     `json:"hasMore"`
	Items    []string `json:"items"`
}

// paginateStrings slices values[] into a page. If prefix is non-empty, only
// values whose lowercase form contains the lowercase prefix are kept (applied
// before paging so totals are accurate). Output is always sorted alphabetically.
//
// Callers' input is never mutated: the filter branch allocates a fresh slice,
// and the no-filter branch clones before sorting. This matters because callers
// routinely pass cache-backed slices (resolver org list, CR listings) that
// would otherwise be reordered as a side effect.
func paginateStrings(values []string, prefix string, page, pageSize int) paginatedStrings {
	if prefix != "" {
		lp := strings.ToLower(prefix)
		filtered := make([]string, 0, len(values))
		for _, v := range values {
			if strings.Contains(strings.ToLower(v), lp) {
				filtered = append(filtered, v)
			}
		}
		values = filtered
	} else {
		values = slices.Clone(values)
	}
	sort.Strings(values)
	if pageSize <= 0 {
		pageSize = 100
	}
	pageSize = clampInt(pageSize, 1, 1000)
	if page < 0 {
		page = 0
	}
	start := min(page*pageSize, len(values))
	end := min(start+pageSize, len(values))
	return paginatedStrings{
		Total:    len(values),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(values),
		Items:    values[start:end],
	}
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
