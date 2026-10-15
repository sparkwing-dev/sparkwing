package nodehost

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// PlanRequest is what [PlanDocument] asks a pipeline process to plan.
type PlanRequest struct {
	Pipeline              string
	Entrypoint            string
	Args                  map[string]string
	Run                   nodeproto.RunContext
	Requires              []string
	Masker                *secrets.Masker
	StartAt, StopAt, Only string
	CheckArgs             func(map[string]string) error
	CheckSecrets          func(context.Context, []string) error
}

// PlannedRun contains validated SDK declarations and the host facts used to plan them.
type PlannedRun struct {
	Secrets  []string
	Requires []string
	Doc      nodeproto.PlanDoc
	JSON     []byte
	Args     map[string]string
}

// PlanDocument plans and validates declarations in a short-lived SDK process.
func PlanDocument(ctx context.Context, cfg SessionConfig, req PlanRequest) (PlannedRun, error) {
	if req.Entrypoint == "" {
		req.Entrypoint = req.Pipeline
	}
	if cfg.Sink == nil {
		cfg.Sink = discardSink{}
	}
	scfg := cfg
	scfg.Binding = nil
	scfg.HoldOutput, scfg.Masker = true, req.Masker
	var err error
	scfg.Env, err = buildSessionEnvironment(scfg, procenv.Plan, sdkRuntime(req.Run, scfg.Runner))
	if err != nil {
		return PlannedRun{}, maskErr(req.Masker, err)
	}
	sess, err := StartSession(ctx, scfg)
	if err != nil {
		return PlannedRun{}, err
	}
	var out PlannedRun
	var known bool
	out.Args, known, err = describeArgs(ctx, sess, req.Entrypoint, req.Args, req.Masker)
	out.Secrets = slices.Clone(sess.secrets)
	out.Requires = slices.Clone(req.Requires)
	if err == nil && req.CheckArgs != nil {
		err = req.CheckArgs(out.Args)
	}
	if err == nil && req.CheckSecrets != nil {
		err = req.CheckSecrets(ctx, slices.Clone(sess.requiredSecrets))
	}
	err = releaseDescribed(sess, known, err)
	if err == nil {
		out.Doc, out.JSON, err = planRun(ctx, sess, req.Entrypoint, out.Args, req.Run)
	}
	if err == nil {
		err = validateStepRange(out.Doc, req.StartAt, req.StopAt)
	}
	if err == nil && req.Only != "" {
		if req.StartAt != "" || req.StopAt != "" {
			err = fmt.Errorf("--only is mutually exclusive with --start-at / --stop-at")
		} else {
			out.Doc, out.JSON, err = cutOutsideOnly(out.Doc, out.JSON, req.Only)
		}
	}
	_, cerr := sess.Close()
	var perr *apierr.Error
	if err == nil && errors.As(cerr, &perr) {
		// safety: a process that broke the protocol after its reply cannot vouch for that plan.
		return PlannedRun{}, maskErr(req.Masker, cerr)
	}
	return out, maskErr(req.Masker, err)
}
