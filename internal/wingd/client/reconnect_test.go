package client

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestReconnectClassifiesHostErrorsFromSpawn(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cause        error
		wantAttempts int
	}{
		{"missing host", ErrNoDaemonHost, 1},
		{"failed host", ErrDaemonHostFailed, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)
			sock, err := wingd.SocketPath(home)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				cl := &Client{sock: sock, opts: Options{Home: home, Backoff: time.Millisecond, Spawn: func(string, string) error {
					attempts++
					return tc.cause
				}}}
				err := cl.reconnect(t.Context(), time.Minute)
				if !errors.Is(err, tc.cause) || attempts != tc.wantAttempts {
					t.Fatalf("reconnect = %v after %d host attempts; want %v after %d", err, attempts, tc.cause, tc.wantAttempts)
				}
			})
		})
	}
}

func TestReconnectRetriesOnlyTransientErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		cl := &Client{reconnectAttempt: func(context.Context) error {
			attempts++
			switch attempts {
			case 1:
				return ErrNoDaemon
			case 2:
				return ErrDaemonUnreachable
			default:
				return ErrNoDaemonHost
			}
		}}
		err := cl.reconnect(t.Context(), time.Minute)
		if !errors.Is(err, ErrNoDaemonHost) || attempts != 3 {
			t.Fatalf("reconnect = %v after %d attempts; want missing host after three", err, attempts)
		}
	})
}
