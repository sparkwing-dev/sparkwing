package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestPausedRunnerClaimsNothingAndResumes(t *testing.T) {
	var logged strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	t.Setenv("SPARKWING_HOME", t.TempDir())
	credentials := t.TempDir()
	disabled := false
	config, err := productionRuntime("v1.2.3").Runner.Factory(t.Context(), RunnerOptions{Config: userconfig.RunnerConfig{Controller: new("http://controller.invalid"), LocalAdmission: &disabled, CredentialsDir: &credentials}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitNode}
	var offered []int
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	transport := client.EngineOptions{URL: "http://controller.invalid", Bearer: fixedBearer("runner"), HTTP: &http.Client{Transport: productionTransport(func(r *http.Request) (*http.Response, error) {
		var request controllerwire.ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if len(request.Kinds) != len(kinds) || request.FreeSlots == nil {
			t.Errorf("pause changed the claim kinds or dropped slots: %+v", request)
		}
		offered = append(offered, *request.FreeSlots)
		switch len(offered) {
		case 2:
			if _, err := runners.SetPaused(credentials, true); err != nil {
				t.Error(err)
			}
		case 4:
			if _, err := runners.SetPaused(credentials, false); err != nil {
				t.Error(err)
			}
		case 6:
			stop()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	controller, err := config.NewController(transport)
	if err != nil {
		t.Fatal(err)
	}
	loop := runners.Loop{Controller: controller, Config: runners.LoopConfig{Version: "v1.2.3", Kinds: kinds, MaxConcurrent: 1, PollInterval: time.Nanosecond}, Host: capacityUnusedHost{}, CheckVersion: func(context.Context, string) error { return nil }}
	if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if want := []int{1, 1, 0, 0, 1, 1}; !slices.Equal(offered, want) {
		t.Fatalf("free slots per poll = %v; want %v", offered, want)
	}
	text := logged.String()
	if strings.Count(text, "runner paused") != 1 || strings.Count(text, "runner resumed") != 1 {
		t.Fatalf("pause transitions were not logged once each:\n%s", text)
	}
}
