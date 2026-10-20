package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestHereCredentialsRequireMatchingLocalTransport(t *testing.T) {
	store := hereCredentials{dir: t.TempDir(), socket: filepath.Join(t.TempDir(), "api.sock")}
	if err := store.save("http://sparkwing-api", "run", "private-bearer"); err != nil {
		t.Fatal(err)
	}
	store.socket = ""
	if _, err := store.load("http://sparkwing-api", "run"); err == nil {
		t.Fatal("unbound transport recovered local authority")
	}
}

func TestLocalControllerClaimDoesNotRetryDialFailure(t *testing.T) {
	options, closeTransport, err := controllerSocketOptions(filepath.Join(t.TempDir(), "api.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	calls := 0
	lost := &net.OpError{Op: "dial", Net: "unix", Err: errors.New("socket unavailable")}
	options.HTTP.Transport.(*controllerSocketTransport).Transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, lost
	}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Claim(t.Context(), controllerwire.ClaimRequest{RunID: "here-run", Kinds: []capability.UnitKind{capability.UnitTrigger}})
	if !errors.Is(err, lost) || calls != 1 {
		t.Fatalf("claim calls=%d error=%v", calls, err)
	}
}

func TestLocalControllerAwardResponseLossIsNotRetried(t *testing.T) {
	for name, call := range map[string]func(context.Context, controllerwire.Controller) error{
		"claim": func(ctx context.Context, c controllerwire.Controller) error {
			_, err := c.Claim(ctx, controllerwire.ClaimRequest{RunID: "here-run", Kinds: []capability.UnitKind{capability.UnitTrigger}})
			return err
		},
		"here intake": func(ctx context.Context, c controllerwire.Controller) error {
			request := controllerwire.CreateRunRequest{IdempotencyKey: "stable-key"}
			request.Request.Options.Runner.Set = true
			request.Request.Options.Runner.Value.Kind = runspec.RunnerHere
			_, err := c.CreateRun(ctx, request)
			return err
		},
		"retry": func(ctx context.Context, c controllerwire.Controller) error {
			_, err := c.Retry(ctx, "here-run", controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "stable-key"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			options, closeTransport, err := controllerSocketOptions(filepath.Join(t.TempDir(), "api.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer closeTransport()
			calls := 0
			done := make(chan error, 1)
			options.HTTP.Transport.(*controllerSocketTransport).Transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
				calls++
				if calls > 1 {
					return nil, errors.New("unexpected second dial")
				}
				connection, server := net.Pipe()
				go func() {
					defer server.Close()
					req, err := http.ReadRequest(bufio.NewReader(server))
					if err == nil {
						_, err = io.Copy(io.Discard, req.Body)
						err = errors.Join(err, req.Body.Close())
					}
					if err == nil {
						_, err = io.WriteString(server, "HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nRetry-After: 0\r\n\r\n"+`{"error":{"code":"unavailable","message":"handoff lost"}}`)
					}
					done <- err
				}()
				return connection, nil
			}
			c, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			err = call(t.Context(), c)
			if serveErr := <-done; serveErr != nil {
				t.Fatal(serveErr)
			}
			if err == nil || calls != 1 {
				t.Fatalf("award calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestHereResumeKeepsLocalSocketBinding(t *testing.T) {
	store := hereCredentials{dir: t.TempDir(), socket: filepath.Join(t.TempDir(), "api.sock")}
	if err := store.save("http://sparkwing-api", "retry-run", "private-bearer"); err != nil {
		t.Fatal(err)
	}
	options, closeTransport, err := controllerSocketOptions(store.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	claimed := 0
	deps := hereDependencies{
		credentials: func() (hereCredentials, error) { return hereCredentials{dir: store.dir}, nil },
		resolve: func(_ context.Context, got client.EngineOptions) (hereRuntime, error) {
			bearer, err := got.Bearer(t.Context())
			if err != nil || bearer != "private-bearer" || got.HTTP != options.HTTP {
				t.Fatal("resume changed its selected local transport or authority")
			}
			return hereRuntime{controller: hereTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claimed++
				return controllerwire.ClaimResponse{}, nil
			}}, bind: func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error) {
				t.Fatal("empty claim constructed a host")
				return hereHostSession{}, nil
			}}, nil
		},
	}
	_, err = resumeHereRun(t.Context(), options, "retry-run", deps)
	if err == nil || claimed != 1 {
		t.Fatalf("claim calls=%d error=%v", claimed, err)
	}
	foreign, closeForeign, err := controllerSocketOptions(filepath.Join(t.TempDir(), "api.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeForeign()
	_, err = resumeHereRun(t.Context(), foreign, "retry-run", deps)
	if err == nil || claimed != 1 {
		t.Fatal("foreign local controller received retained authority")
	}
}

func FuzzHereCredentialsNeverCrossSockets(f *testing.F) {
	f.Add("one", "two", "run", "private-bearer")
	f.Add("a/b", "b/a", "../../run", "bearer")
	f.Fuzz(func(t *testing.T, first, second, run, bearer string) {
		if !utf8.ValidString(first+second+run+bearer) || strings.ContainsRune(first+second, 0) || run == "" || bearer == "" {
			t.Skip()
		}
		root := t.TempDir()
		one := filepath.Join(root, first, "api.sock")
		two := filepath.Join(root, second, "api.sock")
		if one == two {
			t.Skip()
		}
		store := hereCredentials{dir: root, socket: one}
		if err := store.save("http://sparkwing-api", run, bearer); err != nil {
			t.Fatal(err)
		}
		store.socket = two
		if _, err := store.load("http://sparkwing-api", run); err == nil {
			t.Fatal("foreign socket loaded retained authority")
		}
		store.socket = one
		if got, err := store.load("http://sparkwing-api/", run); err != nil || got != bearer {
			t.Fatal("own socket failed to recover retained authority")
		}
	})
}
