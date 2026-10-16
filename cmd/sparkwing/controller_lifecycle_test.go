package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

type controllerStartLifecycle struct {
	wingd.LocalLifecycle
	start func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error)
}

func (f controllerStartLifecycle) EnsureStarted(ctx context.Context, options wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	return f.start(ctx, options)
}

func TestLocalControllerOptionsReusesElectedOwner(t *testing.T) {
	home := t.TempDir()
	options, closeTransport, err := localControllerOptions(t.Context(), home,
		func(_ context.Context, got string) (wingd.LocalStatus, error) {
			if got != home {
				t.Fatal("query changed home")
			}
			return wingd.LocalStatus{Running: true, PID: 17, Version: "older-supported-engine"}, nil
		}, func(context.Context) (dashboardServices, error) {
			t.Fatal("ready owner constructed another runtime")
			return dashboardServices{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if options.URL != wingdclient.APIBaseURL || options.HTTP.Transport.(*controllerSocketTransport).socket != socket || options.Bearer != nil {
		t.Fatal("owner transport changed its identity")
	}
	if err := options.HTTP.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("local transport allowed a redirect")
	}
}

func TestLocalControllerOptionsStartsThroughBoundLifecycle(t *testing.T) {
	home := t.TempDir()
	want := wingd.LocalStartOptions{Home: home, Executable: filepath.Join(home, "sparkwing"), Version: "v1.2.3"}
	calls := 0
	options, closeTransport, err := localControllerOptions(t.Context(), home,
		func(context.Context, string) (wingd.LocalStatus, error) {
			return wingd.LocalStatus{}, wingdclient.ErrNoDaemon
		},
		func(context.Context) (dashboardServices, error) {
			return dashboardServices{options: want, lifecycle: controllerStartLifecycle{start: func(ctx context.Context, got wingd.LocalStartOptions) (wingd.LocalStatus, error) {
				calls++
				if !reflect.DeepEqual(got, want) || ctx.Err() != nil {
					t.Fatal("startup changed captured options")
				}
				return wingd.LocalStatus{Running: true, PID: 17, Version: want.Version}, nil
			}}}, nil
		})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	defer closeTransport()
	if options.HTTP == nil {
		t.Fatal("missing ready transport")
	}
}

func TestLocalControllerOptionsRefusesUncertainOrOtherOwner(t *testing.T) {
	home := t.TempDir()
	failure := errors.New("owner unreachable")
	_, _, err := localControllerOptions(t.Context(), home,
		func(context.Context, string) (wingd.LocalStatus, error) { return wingd.LocalStatus{}, failure },
		func(context.Context) (dashboardServices, error) {
			t.Fatal("uncertain owner was replaced")
			return dashboardServices{}, nil
		})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	_, _, err = localControllerOptions(t.Context(), home,
		func(context.Context, string) (wingd.LocalStatus, error) {
			return wingd.LocalStatus{}, wingdclient.ErrNoDaemon
		},
		func(context.Context) (dashboardServices, error) {
			return dashboardServices{options: wingd.LocalStartOptions{Home: t.TempDir()}, lifecycle: controllerStartLifecycle{start: func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
				t.Fatal("other home was started")
				return wingd.LocalStatus{}, nil
			}}}, nil
		})
	if err == nil {
		t.Fatal("another state home was accepted")
	}
}

func TestLocalControllerOptionsNamesStartupFailure(t *testing.T) {
	home := t.TempDir()
	failure := errors.New("executable refused")
	_, _, err := localControllerOptions(t.Context(), home,
		func(context.Context, string) (wingd.LocalStatus, error) {
			return wingd.LocalStatus{}, wingdclient.ErrNoDaemon
		}, func(context.Context) (dashboardServices, error) {
			return dashboardServices{options: wingd.LocalStartOptions{Home: home}, lifecycle: controllerStartLifecycle{start: func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
				return wingd.LocalStatus{}, failure
			}}}, nil
		})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "cannot start local controller") {
		t.Fatalf("startup diagnostic: %v", err)
	}
}
