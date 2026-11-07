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

// safety: one warning per distinct cause shows a CLI upgrade that left the runner behind in the runner's
// log before a claimed job fails on it, without repeating on every claim poll.
type admissionWarning struct {
	mu   sync.Mutex
	last string
}

func (w *admissionWarning) observe(version string, err error) {
	var message string
	if explained := nodehost.RunnerAdmissionUnavailable(version, err); explained != nil {
		message = explained.Error()
	}
	w.mu.Lock()
	changed := message != w.last
	w.last = message
	w.mu.Unlock()
	if changed && message != "" {
		slog.Warn("local admission is unavailable; claimed jobs fail until it is fixed", "err", message)
	}
}

func (c nativeCapacityController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	if slices.Contains(request.Kinds, capability.UnitNode) {
		state, err := wingdclient.Query(ctx, wingdclient.Options{Home: c.home, Version: c.version})
		c.unavailable.observe(c.version, err)
		if err != nil {
			// safety: a missing daemon must not prevent planning; the awarded host still enforces admission.
			return c.Controller.Claim(ctx, request)
		}
		if !wingdclient.HasJobCapacity(state) {
			if len(request.Kinds) == 1 {
				request.FreeSlots = new(int)
			} else {
				request.Kinds = slices.DeleteFunc(slices.Clone(request.Kinds), func(kind capability.UnitKind) bool { return kind == capability.UnitNode })
			}
		}
	}
	return c.Controller.Claim(ctx, request)
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
