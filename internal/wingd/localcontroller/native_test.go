package localcontroller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestNativeServicesRefusesUnknownAuthorityBeforeOpeningResources(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		return serviceFixture(b), nil
	})
	home := filepath.Join(t.TempDir(), "unopened")
	principal := capability.Principal{
		Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "forged"},
		Capability: capability.Capability{Team: string(binding.Tenant.Team()), Binding: capability.Binding{RunID: "unavailable"}},
	}
	refreshed := false
	services, err := NativeServices(t.Context(), NativeConfig{Local: func(store.NodeRow) bool { return true }, Home: home, Version: "v0.71.0", Store: binding.Tenant, Parent: c.store, Bindings: c.store, Renew: c.store.RenewUnit, Principal: principal, Refresh: func(ctx context.Context) (capability.Principal, error) {
		refreshed = true
		route, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/heartbeat")
		_, err := capability.NewAuthenticator(c.store).Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))}, route)
		return capability.Principal{}, err
	}})
	if err == nil || services.Config.Store != nil || services.Close != nil {
		t.Fatalf("unknown authority produced native services: %+v, %v", services, err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unauthenticated setup created private resources: %v", err)
	}
	if !refreshed {
		t.Fatal("native construction did not reauthorize its award")
	}
}

type nativeAuthentication struct {
	snapshot capability.AuthenticationSnapshot
}

func (a *nativeAuthentication) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, _ capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if len(a.snapshot.Authorities) == 0 || lookup.Class != capability.CredentialBearer || lookup.Audience != capability.Controller || lookup.Digest != a.snapshot.Authorities[0].Principal.Capability.Digest {
		return capability.AuthenticationSnapshot{}, errors.New("unknown parent credential")
	}
	return a.snapshot, context.Cause(ctx)
}

func (*nativeAuthentication) CheckBinding(context.Context, capability.Principal, capability.BindingRule, capability.Target) error {
	return nil
}

func TestLocalStateSubscriptionJoinsAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		subscription, err := localStateSubscription(ctx, "run")
		if err != nil {
			t.Fatal(err)
		}
		<-subscription.Changed
		cancel()
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

type processListener struct {
	connection net.Conn
	closed     chan struct{}
	once       sync.Once
}

func (l *processListener) Accept() (net.Conn, error) {
	if l.connection != nil {
		connection := l.connection
		l.connection = nil
		return connection, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *processListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*processListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestNativeHTTPJoinsBeforeReleasingAuthority(t *testing.T) {
	for _, hijack := range []bool{false, true} {
		t.Run(fmt.Sprint(hijack), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, server := net.Pipe()
				defer client.Close()
				listener := &processListener{connection: server, closed: make(chan struct{})}
				entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
				closed := false
				closeFailure := errors.New("authority cleanup failed")
				start, closeHTTP := localProcessHTTP(t.Context(), listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if hijack {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer connection.Close()
						close(entered)
						if _, err := connection.Read(make([]byte, 1)); err == nil {
							t.Error("hijacked connection stayed open")
						}
					} else {
						close(entered)
						<-r.Context().Done()
					}
					<-release
					close(exited)
				}), func() error {
					select {
					case <-exited:
					default:
						t.Error("authority closed before handler joined")
					}
					closed = true
					return closeFailure
				})
				start()
				if _, err := io.WriteString(client, "GET /node/v1/masks HTTP/1.1\r\nHost: private\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				<-entered
				done := make(chan error, 1)
				go func() { done <- closeHTTP() }()
				synctest.Wait()
				if closed {
					t.Fatal("cleanup did not join its handler")
				}
				select {
				case err := <-done:
					t.Fatalf("cleanup returned early: %v", err)
				default:
				}
				close(release)
				if err := <-done; !errors.Is(err, closeFailure) {
					t.Fatalf("cleanup lost authority failure: %v", err)
				}
				if err := closeHTTP(); !errors.Is(err, closeFailure) {
					t.Fatalf("repeated cleanup: %v", err)
				}
			})
		})
	}
}

func FuzzNativeHTTPStartCloseOrdering(f *testing.F) {
	f.Add(uint8(0), uint8(1))
	f.Add(uint8(3), uint8(2))
	f.Fuzz(func(t *testing.T, starts, closes uint8) {
		listener := &processListener{closed: make(chan struct{})}
		released := 0
		start, closeHTTP := localProcessHTTP(t.Context(), listener, http.NotFoundHandler(), func() error { released++; return nil })
		for range int(starts % 8) {
			start()
		}
		for range int(closes%8) + 1 {
			if err := closeHTTP(); err != nil {
				t.Fatal(err)
			}
			start()
		}
		if released != 1 {
			t.Fatalf("authority released %d times", released)
		}
		select {
		case <-listener.closed:
		default:
			t.Fatal("private listener stayed open")
		}
	})
}
