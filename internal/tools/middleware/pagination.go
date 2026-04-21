package middleware

import (
	"sort"
	"strings"
)

// PaginatedStrings is the JSON projection used by every "list_*" tool that
// returns a flat list of strings (metric names, label values, tag values…).
type PaginatedStrings struct {
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
	HasMore  bool     `json:"hasMore"`
	Items    []string `json:"items"`
}

// PaginateStrings slices values[] into a page. If prefix is non-empty, only
// values whose lowercase form contains the lowercase prefix are kept (applied
// before paging so totals are accurate). Output is always sorted alphabetically.
//
// A defensive copy is made before sorting so callers can pass a slice backed
// by a cache (e.g. the resolver's org list) without having their cache
// reordered as a side effect.
func PaginateStrings(values []string, prefix string, page, pageSize int) PaginatedStrings {
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
		values = append([]string(nil), values...)
	}
	sort.Strings(values)
	if pageSize <= 0 {
		pageSize = 100
	}
	pageSize = ClampInt(pageSize, 1, 1000)
	if page < 0 {
		page = 0
	}
	start := min(page*pageSize, len(values))
	end := min(start+pageSize, len(values))
	return PaginatedStrings{
		Total:    len(values),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(values),
		Items:    values[start:end],
	}
}
