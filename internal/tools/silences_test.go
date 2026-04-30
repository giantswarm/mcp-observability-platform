package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func buildSilences(specs []struct {
	ID, State, EndsAt, CreatedBy string
	Matchers                     int
}) json.RawMessage {
	silences := make([]amSilence, 0, len(specs))
	for _, s := range specs {
		matchers := make([]amMatcher, s.Matchers)
		for i := range matchers {
			matchers[i] = amMatcher{Name: fmt.Sprintf("k%d", i), Value: "v", IsEqual: true}
		}
		sil := amSilence{
			ID:        s.ID,
			EndsAt:    s.EndsAt,
			CreatedBy: s.CreatedBy,
			Matchers:  matchers,
		}
		sil.Status.State = s.State
		silences = append(silences, sil)
	}
	b, _ := json.Marshal(silences)
	return b
}

func TestPaginateSilences_DefaultStateActive_SortsByEndsAtAsc(t *testing.T) {
	raw := buildSilences([]struct {
		ID, State, EndsAt, CreatedBy string
		Matchers                     int
	}{
		{"a", "active", "2026-05-01T00:00:00Z", "alice", 1},
		{"b", "active", "2026-04-30T00:00:00Z", "bob", 2},
		{"c", "expired", "2026-04-01T00:00:00Z", "carol", 1},
		{"d", "pending", "2026-06-01T00:00:00Z", "dave", 1},
	})
	res, err := paginateSilences(raw, "", 0, 50)
	if err != nil {
		t.Fatalf("paginateSilences: %v", err)
	}
	j, _ := json.Marshal(res)
	var out struct {
		Items []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(j, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 2 {
		t.Fatalf("total = %d, want 2 (active only)", out.Total)
	}
	got := []string{out.Items[0].ID, out.Items[1].ID}
	want := []string{"b", "a"} // soonest endsAt first
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestPaginateSilences_StateFilter(t *testing.T) {
	raw := buildSilences([]struct {
		ID, State, EndsAt, CreatedBy string
		Matchers                     int
	}{
		{"a", "active", "2026-05-01T00:00:00Z", "", 0},
		{"b", "expired", "2026-04-01T00:00:00Z", "", 0},
		{"c", "pending", "2026-06-01T00:00:00Z", "", 0},
	})
	for _, tc := range []struct {
		state    string
		wantIDs  []string
		wantSize int
	}{
		{"active", []string{"a"}, 1},
		{"pending", []string{"c"}, 1},
		{"expired", []string{"b"}, 1},
		{"all", []string{"b", "a", "c"}, 3},
	} {
		t.Run(tc.state, func(t *testing.T) {
			res, err := paginateSilences(raw, tc.state, 0, 50)
			if err != nil {
				t.Fatalf("paginateSilences: %v", err)
			}
			j, _ := json.Marshal(res)
			var out struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
				Total int `json:"total"`
			}
			_ = json.Unmarshal(j, &out)
			if out.Total != tc.wantSize {
				t.Errorf("%s: total = %d, want %d", tc.state, out.Total, tc.wantSize)
			}
			got := make([]string, 0, len(out.Items))
			for _, it := range out.Items {
				got = append(got, it.ID)
			}
			if !reflect.DeepEqual(got, tc.wantIDs) {
				t.Errorf("%s: ids = %v, want %v", tc.state, got, tc.wantIDs)
			}
		})
	}
}

func TestPaginateSilences_RejectsUnknownState(t *testing.T) {
	raw := json.RawMessage(`[]`)
	if _, err := paginateSilences(raw, "bogus", 0, 50); err == nil {
		t.Fatal("expected error for unknown state, got nil")
	}
}

func TestPaginateSilences_PageBoundaries(t *testing.T) {
	specs := []struct {
		ID, State, EndsAt, CreatedBy string
		Matchers                     int
	}{}
	for i := range 5 {
		specs = append(specs, struct {
			ID, State, EndsAt, CreatedBy string
			Matchers                     int
		}{
			ID:     fmt.Sprintf("id-%02d", i),
			State:  "active",
			EndsAt: fmt.Sprintf("2026-05-%02dT00:00:00Z", i+1),
		})
	}
	raw := buildSilences(specs)
	p2, _ := paginateSilences(raw, "active", 2, 2) // last item, no more
	p3, _ := paginateSilences(raw, "active", 3, 2) // past end
	check := func(name string, res any, wantLen int, wantHasMore bool) {
		b, _ := json.Marshal(res)
		var out struct {
			Items   []any `json:"items"`
			HasMore bool  `json:"hasMore"`
		}
		_ = json.Unmarshal(b, &out)
		if len(out.Items) != wantLen {
			t.Errorf("%s items = %d, want %d", name, len(out.Items), wantLen)
		}
		if out.HasMore != wantHasMore {
			t.Errorf("%s hasMore = %v, want %v", name, out.HasMore, wantHasMore)
		}
	}
	check("p2", p2, 1, false)
	check("p3", p3, 0, false)
}

func TestPaginateSilences_MalformedBody(t *testing.T) {
	if _, err := paginateSilences(json.RawMessage("not-json"), "active", 0, 50); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}
}
