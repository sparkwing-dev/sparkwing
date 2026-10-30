package controller

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCollectionPagesWalkStableKeys(t *testing.T) {
	type row struct{ Key, Value string }
	rows := []row{{"d", "last"}, {"a", "first"}, {"b", "second"}, {"c", "third"}}
	var got []string
	cursor := ""
	for range len(rows) {
		r := httptest.NewRequest("GET", "/api/v1/example?limit=1&cursor="+url.QueryEscape(cursor), nil)
		items, page, err := pageCollection(r, capability.Authorized{}, rows, func(row row) string { return row.Key })
		if err != nil || len(items) != 1 {
			t.Fatalf("page: %v %+v %+v", err, items, page)
		}
		got = append(got, items[0].Value)
		if page.Truncated != (len(got) < len(rows)) || page.Truncated && (page.NextCursor == "" || page.NextCursor == cursor) {
			t.Fatalf("page did not advance: %+v", page)
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(got, []string{"first", "second", "third", "last"}) || cursor != "" {
		t.Fatalf("walk = %v cursor=%q", got, cursor)
	}
}

func TestCollectionCursorRetainsScopeAndSurvivesBoundaryDeletion(t *testing.T) {
	a := capability.Authorized{Target: capability.Target{Team: "a"}}
	r := httptest.NewRequest("GET", "/api/v1/example?kind=runner&limit=1", nil)
	key := func(row string) string { return row }
	_, page, err := pageCollection(r, a, []string{"a", "b", "c"}, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/other?kind=runner", "/api/v1/example?kind=machine", "/api/v1/example?kind=runner&kind=runner",
	} {
		r := httptest.NewRequest("GET", path+"&cursor="+url.QueryEscape(page.NextCursor), nil)
		if _, _, err := pageCollection(r, a, []string{"a", "b", "c"}, key); err == nil {
			t.Fatalf("accepted cursor with different selection: %s", path)
		}
	}
	r = httptest.NewRequest("GET", "/api/v1/example?kind=runner&cursor="+url.QueryEscape(page.NextCursor), nil)
	other := a
	other.Target.Team = "b"
	if _, _, err := pageCollection(r, other, []string{"a", "b", "c"}, key); err == nil {
		t.Fatal("accepted another team's cursor")
	}
	items, last, err := pageCollection(r, a, []string{"b", "c"}, key)
	if err != nil || !reflect.DeepEqual(items, []string{"b", "c"}) || last.Truncated {
		t.Fatalf("deleted boundary: %v %+v %+v", err, items, last)
	}
}

func TestCollectionPageDefaultsAndLimitGuidance(t *testing.T) {
	rows := make([]string, 201)
	for i := range rows {
		rows[i] = fmt.Sprintf("%03d", i)
	}
	key := func(row string) string { return row }
	for _, tc := range []struct {
		query string
		want  int
	}{{"", 50}, {"?limit=200", 200}, {"?limit=1", 1}} {
		r := httptest.NewRequest("GET", "/list"+tc.query, nil)
		items, page, err := pageCollection(r, capability.Authorized{}, rows, key)
		if err != nil || len(items) != tc.want || !page.Truncated {
			t.Fatalf("query %s: %v items=%d page=%+v", tc.query, err, len(items), page)
		}
	}
	for _, query := range []string{"limit=201", "limit=0", "limit=-1", "limit=bad", "limit=1&limit=2"} {
		r := httptest.NewRequest("GET", "/list?"+query, nil)
		_, _, err := pageCollection(r, capability.Authorized{}, rows, key)
		if err == nil || !strings.Contains(err.Error(), "200") || !strings.Contains(err.Error(), "requested") || !strings.Contains(err.Error(), "contact support") {
			t.Fatalf("query %s lacks limit guidance: %v", query, err)
		}
	}
}
