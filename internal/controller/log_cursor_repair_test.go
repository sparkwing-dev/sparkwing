package controller

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestLogCursorRefusalExplainsBothRecoveryPaths(t *testing.T) {
	logs, _, state := committedDashboardLogs(t, []byte("first\nsecond\n"))
	options, _, service := memberObservationOptions(t, state, logs.objs.(blob.ObjectBackend))
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	options.URL, options.HTTP = server.URL, server.Client()
	reader, err := client.NewLogReader(options)
	if err != nil {
		t.Fatal(err)
	}
	original := controllerwire.LogQuery{Job: "job", NoEvents: true}
	page, err := reader.ReadLogs(t.Context(), "run", original)
	if err != nil || len(page.Records) != 2 || page.NextCursor == "" {
		t.Fatalf("original selection: %+v %v", page, err)
	}
	for _, cursor := range []string{page.NextCursor, "invalid-cursor"} {
		changed := original
		changed.Grep, changed.Cursor = "first", cursor
		_, err := reader.ReadLogs(t.Context(), "run", changed)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != apierr.BadRequest {
			t.Fatalf("cursor refusal: %v", err)
		}
		if refusal.Message != "log cursor does not match this selection" {
			t.Errorf("refusal changed: %+v", refusal)
		}
		for _, part := range []string{"Remove", "cursor", "restart", "same run", "job", "filters"} {
			if !strings.Contains(refusal.Hint, part) {
				t.Errorf("hint lacks %q: %+v", part, refusal)
			}
		}
		if strings.Contains(refusal.Hint, cursor) {
			t.Fatal("hint echoes rejected cursor")
		}
		changed.Cursor = ""
		repaired, err := reader.ReadLogs(t.Context(), "run", changed)
		if err != nil || len(repaired.Records) != 1 || repaired.Records[0].Line != "first" {
			t.Fatalf("restart repair: %+v %v", repaired, err)
		}
	}
	original.Cursor = page.NextCursor
	resumed, err := reader.ReadLogs(t.Context(), "run", original)
	if err != nil || len(resumed.Records) != 0 || resumed.NextCursor != page.NextCursor {
		t.Fatalf("same-selection repair: %+v %v", resumed, err)
	}
}
