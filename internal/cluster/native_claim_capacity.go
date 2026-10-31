package cluster

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

type nativeCapacityController struct {
	controllerwire.Controller
	home, version string
}

func (c nativeCapacityController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	if slices.Contains(request.Kinds, capability.UnitNode) {
		state, err := wingdclient.Query(ctx, wingdclient.Options{Home: c.home, Version: c.version})
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
		return nativeCapacityController{Controller: controller, home: home, version: version}, nil
	}
}
