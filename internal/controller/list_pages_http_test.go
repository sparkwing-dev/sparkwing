package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSecretInventoryHTTPPagesBeyondDefault(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for i := range 53 {
		if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `INSERT INTO secrets(team,name,value,principal,created_at,updated_at) VALUES ('default',?,'sealed','owner',1,1)`, fmt.Sprintf("KEY_%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	seen, cursor := map[string]bool{}, ""
	for pageNumber := range 2 {
		path := "/api/v1/secrets"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		status, body, _ := secretsRequest(t, f, http.MethodGet, path, "", nil)
		var page struct {
			Secrets    []listedSecret `json:"secrets"`
			NextCursor string         `json:"next_cursor"`
			Truncated  bool           `json:"truncated"`
		}
		if status != http.StatusOK || json.Unmarshal([]byte(body), &page) != nil || page.Truncated != (pageNumber == 0) {
			t.Fatalf("page %d: HTTP %d %s", pageNumber, status, body)
		}
		want := 50
		if pageNumber == 1 {
			want = 3
		}
		if len(page.Secrets) != want {
			t.Fatalf("page %d has %d secrets, want %d", pageNumber, len(page.Secrets), want)
		}
		for _, secret := range page.Secrets {
			if seen[secret.Name] {
				t.Fatalf("secret repeated across pages: %s", secret.Name)
			}
			seen[secret.Name] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 53 || cursor != "" {
		t.Fatalf("inventory incomplete: %d, cursor %q", len(seen), cursor)
	}
}
