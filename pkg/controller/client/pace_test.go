package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func deadToken(state string) error {
	return fmt.Errorf("claim: %w", &TokenDeadError{State: state, Message: "token is " + state})
}

func TestPacer_DeadTokenParksForAnHourAndSaysSoOnce(t *testing.T) {
	for _, state := range []string{"revoked", "expired"} {
		t.Run(state, func(t *testing.T) {
			p := NewPacer(500 * time.Millisecond)
			first := p.Failure(deadToken(state))
			if !first.Parked || first.Dead == nil || first.Dead.State != state {
				t.Fatalf("first dead-token failure = %+v, want parked with the refusal to log", first)
			}
			if first.Wait < DeadTokenRetry || first.Wait > DeadTokenRetry+DeadTokenRetry/4 {
				t.Fatalf("parked wait = %s, want about %s", first.Wait, DeadTokenRetry)
			}
			again := p.Failure(deadToken(state))
			if !again.Parked || again.Dead != nil {
				t.Fatalf("second dead-token failure = %+v, want parked without a second loud line", again)
			}
		})
	}
}

func TestPacer_OnlyAnAnsweredRequestUnparks(t *testing.T) {
	p := NewPacer(time.Second)
	p.Failure(deadToken("revoked"))
	if pace := p.Failure(errors.New("dial tcp: connection refused")); !pace.Parked || pace.Wait < DeadTokenRetry {
		t.Fatalf("a network fault while parked = %+v, want the loop to stay parked", pace)
	}
	if !p.Success() {
		t.Fatal("Success after a park did not report the resume")
	}
	if p.Success() {
		t.Fatal("a second Success reported a resume that already happened")
	}
	if pace := p.Failure(errors.New("controller 500: boom")); pace.Parked || pace.Wait > 2*FailureBackoffBase {
		t.Fatalf("the first failure after resuming = %+v, want a fresh short backoff", pace)
	}
}

func TestPacer_FailuresDoubleWithJitterToTheCap(t *testing.T) {
	p := NewPacer(0)
	var prev time.Duration
	for i := range 20 {
		pace := p.Failure(errors.New("controller 502: bad gateway"))
		if pace.Parked || pace.Shed {
			t.Fatalf("failure %d = %+v, want an ordinary backoff", i, pace)
		}
		ceiling := min(FailureBackoffBase<<i, FailureBackoffCap)
		if pace.Wait > ceiling || pace.Wait < ceiling*3/4 {
			t.Fatalf("failure %d waited %s, want within the quarter below %s", i, pace.Wait, ceiling)
		}
		if ceiling < FailureBackoffCap && i > 0 && pace.Wait <= prev {
			t.Fatalf("failure %d waited %s, no longer than the %s before it", i, pace.Wait, prev)
		}
		prev = pace.Wait
	}
}

func TestPacer_NeverWaitsLessThanTheLoopsCadence(t *testing.T) {
	p := NewPacer(10 * time.Second)
	if pace := p.Failure(errors.New("controller 500: boom")); pace.Wait < 10*time.Second {
		t.Fatalf("wait = %s, below the 10s poll cadence", pace.Wait)
	}
}

func TestPacer_AFloorAboveTheCapIsClamped(t *testing.T) {
	p := NewPacer(10 * time.Minute)
	for i := range 5 {
		if pace := p.Failure(errors.New("controller 500: boom")); pace.Wait > FailureBackoffCap {
			t.Fatalf("failure %d waited %s, above the %s cap", i, pace.Wait, FailureBackoffCap)
		}
	}
}

func TestPacer_RateLimitHonoursRetryAfter(t *testing.T) {
	p := NewPacer(0)
	pace := p.Failure(&RateLimitedError{RetryAfter: 20 * time.Second, Err: errors.New("controller 429")})
	if !pace.Shed || pace.Parked {
		t.Fatalf("a 429 = %+v, want shed, not parked", pace)
	}
	if pace.Wait < 20*time.Second {
		t.Fatalf("a 429 naming 20s waited %s", pace.Wait)
	}
}

func TestPacer_AnotherUnauthorizedBacksOffWithoutParking(t *testing.T) {
	p := NewPacer(0)
	pace := p.Failure(errors.New("controller 401: unknown token"))
	if pace.Parked || pace.Dead != nil || pace.Wait > FailureBackoffBase {
		t.Fatalf("a 401 without a token state = %+v, want the ordinary first backoff", pace)
	}
}

func TestClassifyHTTPError_ReadsTheTokenState(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		state  string
	}{
		{"revoked", http.StatusUnauthorized, `{"error":"unauthenticated","token_state":"revoked","message":"token is revoked"}`, "revoked"},
		{"expired", http.StatusUnauthorized, `{"error":"unauthenticated","token_state":"expired","message":"token is expired"}`, "expired"},
		{"unknown token", http.StatusUnauthorized, `{"error":"unauthenticated","message":"unknown token"}`, ""},
		{"state outside the enum", http.StatusUnauthorized, `{"error":"unauthenticated","token_state":"sleepy"}`, ""},
		{"not json", http.StatusUnauthorized, `unauthorized`, ""},
		{"a 403 naming a state", http.StatusForbidden, `{"error":"forbidden","token_state":"revoked"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			dead := AsTokenDead(classifyHTTPError(resp))
			ok := dead != nil
			if tc.state == "" {
				if ok {
					t.Fatalf("classified %q as a dead token (%s)", tc.body, dead.State)
				}
				return
			}
			if !ok || dead.State != tc.state {
				t.Fatalf("classified %q as %v, want a %s token", tc.body, dead, tc.state)
			}
		})
	}
}

func TestTokenPrefix_NeverPrintsTheSecret(t *testing.T) {
	if got := TokenPrefix("swr_abcdefgh_secretsecretsecret"); strings.Contains(got, "secret") || got == "" {
		t.Fatalf("TokenPrefix = %q", got)
	}
	if got := TokenPrefix("short"); got != "(malformed)" {
		t.Fatalf("TokenPrefix of a short token = %q, want (malformed)", got)
	}
}
