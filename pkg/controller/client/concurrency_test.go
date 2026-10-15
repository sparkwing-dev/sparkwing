package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type concurrencyTransport struct {
	status int
	body   string
}

func (tr concurrencyTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(tr.status)
	_, _ = w.Write([]byte(tr.body))
	return w.Result(), nil
}

func TestConcurrencyClientDomainErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
		code   apierr.Code
	}{
		{"missing", 404, `{"error":{"code":"not_found","message":"holder absent"}}`, store.ErrNotFound, apierr.NotFound},
		{"lost", 409, `{"error":{"code":"lock_held","message":"claim changed"}}`, store.ErrLockHeld, apierr.LockHeld},
		{"superseded", 409, `{"error":{"code":"conflict","message":"superseded","details":{"reason":"concurrency_superseded"}}}`, store.ErrConcurrencySuperseded, apierr.Conflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New("http://controller.invalid", &http.Client{Transport: concurrencyTransport{tc.status, tc.body}})
			_, err := c.ObserveSlot(context.Background(), "group", "holder")
			var failure *apierr.Error
			if !errors.Is(err, tc.want) || !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestConcurrencyClientRejectsNonPolicyErrors(t *testing.T) {
	for _, body := range []string{`{"error":"not found"}`, `{"error":null}`, `held by another holder`} {
		c := New("http://controller.invalid", &http.Client{Transport: concurrencyTransport{409, body}})
		_, err := c.HeartbeatSlot(context.Background(), "group", "holder", 0)
		if err == nil || errors.Is(err, store.ErrLockHeld) {
			t.Fatalf("untyped error accepted: %s: %v", body, err)
		}
	}
}

func TestConcurrencyClientDistinguishesAdmissionFromRateLimits(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"rate_limited","message":"retry later"}}`, `{"kind":"granted","granted":true}`} {
		c := New("http://controller.invalid", &http.Client{Transport: concurrencyTransport{429, body}})
		if result, err := c.AcquireSlot(context.Background(), "group", AcquireSlotRequest{RunID: "run", HolderID: "holder"}); err == nil || result != nil {
			t.Fatalf("429 accepted: %+v %v", result, err)
		}
	}
}
