package client

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestDeleteTeamSendsTheConfirmationAndReadsTheDeletion(t *testing.T) {
	var method, path string
	var body map[string]string
	c := testEngine(t, func(r *http.Request) (*http.Response, error) {
		method, path = r.Method, r.URL.Path
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body %q: %v", raw, err)
		}
		return engineResponse(http.StatusAccepted, `{"slug":"acme","state":"pending","requested_at":1,"finished_at":null,"attempts":0,"last_error":""}`), nil
	})
	got, err := c.DeleteTeam(t.Context(), "acme")
	if err != nil || got.Slug != "acme" || got.State != "pending" {
		t.Fatalf("DeleteTeam = %+v %v", got, err)
	}
	if method != http.MethodDelete || path != "/api/v1/team" || body["confirm_slug"] != "acme" {
		t.Fatalf("request = %s %s %v", method, path, body)
	}
}

func TestDeleteTeamRefusesAnEmptyConfirmationAndAnInvalidReply(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("an empty confirmation reached HTTP")
		return nil, nil
	})
	if _, err := c.DeleteTeam(t.Context(), ""); err == nil {
		t.Fatal("accepted an empty confirmation")
	}
	c = testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(http.StatusAccepted, `{}`), nil
	})
	if _, err := c.DeleteTeam(t.Context(), "acme"); err == nil {
		t.Fatal("accepted a deletion with no slug or state")
	}
}
