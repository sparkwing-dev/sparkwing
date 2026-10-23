package client

import (
	"errors"
	"math"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestOverflowClientRejectsInvalidAllowancesBeforeHTTP(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid limit reached HTTP")
		return nil, nil
	})
	for _, value := range []int64{-1, math.MaxInt64/60 + 1} {
		_, err := c.SetOverflowLimit(t.Context(), &value)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.BadRequest {
			t.Fatalf("limit %d: %v", value, err)
		}
	}
}

func TestOverflowClientRejectsInvalidControlReceipts(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"monthly_minutes":-1,"used_seconds":0,"period_start":"2026-10-01T00:00:00Z"}`,
		`{"monthly_minutes":null,"used_seconds":-1,"period_start":"2026-10-01T00:00:00Z"}`,
		`{"monthly_minutes":null,"used_seconds":0,"period_start":"0001-01-01T00:00:00Z"}`,
		`{"monthly_minutes":null,"used_seconds":0,"period_start":"2026-10-02T00:00:00Z"}`,
		`{"monthly_minutes":153722867280912931,"used_seconds":0,"period_start":"2026-10-01T00:00:00Z"}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ReadOverflowControl(t.Context()); err == nil {
			t.Fatalf("accepted invalid control: %s", body)
		}
		if _, err := c.SetOverflowLimit(t.Context(), nil); err == nil {
			t.Fatalf("accepted invalid update: %s", body)
		}
	}
}
