package cluster

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

type nativeCapacityController struct {
	controllerwire.Controller
	home, version string
	unavailable   *admissionWarning
}

// safety: one line per distinct cause explains why the runner stopped taking jobs in the runner's log,
// without repeating on every claim poll; a second line says when it takes jobs again.
type admissionWarning struct {
	mu   sync.Mutex
	last string
}

// safety: a definite cause is a missing or incompatible local controller, under which every claimed
// job would fail; any other read failure leaves the decision to admission at the host.
func (w *admissionWarning) observe(version string, err error) bool {
	var message string
	if explained := nodehost.RunnerAdmissionUnavailable(version, err); explained != nil {
		message = explained.Error()
	}
	w.mu.Lock()
	previous := w.last
	w.last = message
	w.mu.Unlock()
	switch {
	case message != "" && message != previous:
		slog.Warn("runner idle: it takes no jobs until this is fixed; planning continues", "err", message)
	case message == "" && previous != "" && err == nil:
		slog.Info("runner takes jobs again: the local controller is reachable")
	}
	return message != ""
}

func (c nativeCapacityController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	if slices.Contains(request.Kinds, capability.UnitNode) {
		state, err := wingdclient.Query(ctx, wingdclient.Options{Home: c.home, Version: c.version})
		definite := c.unavailable.observe(c.version, err)
		switch {
		case definite:
			request = withoutJobCapacity(request)
		case err != nil:
			// safety: an unexplained read failure leaves the claim to admission at the host, which stays authoritative.
		case !wingdclient.HasJobCapacity(state):
			request = withoutJobCapacity(request)
		}
	}
	return c.Controller.Claim(ctx, request)
}

func withoutJobCapacity(request controllerwire.ClaimRequest) controllerwire.ClaimRequest {
	if len(request.Kinds) == 1 {
		request.FreeSlots = new(int)
	} else {
		request.Kinds = slices.DeleteFunc(slices.Clone(request.Kinds), func(kind capability.UnitKind) bool { return kind == capability.UnitNode })
	}
	return request
}

func nativeCapacityClient(home, version string) func(client.EngineOptions) (controllerwire.Controller, error) {
	return func(options client.EngineOptions) (controllerwire.Controller, error) {
		controller, err := client.NewEngineClient(options)
		if err != nil {
			return nil, err
		}
		return nativeCapacityController{Controller: controller, home: home, version: version, unavailable: &admissionWarning{}}, nil
	}
}
