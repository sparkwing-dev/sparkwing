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
	Pipeline   string
	Entrypoint string
	// Args are the run's flag values; describe's defaults fill in the rest.
	Args map[string]string
	Run  nodeproto.RunContext
	// Requires are the placement labels the host reads from the pipeline's
	// YAML; they join the plan's own.
	Requires []string
	// Masker, when set, has the value of every flag describe marks secret
	// registered before plan code runs.
	Masker *secrets.Masker
	// StartAt and StopAt must name steps of the plan, and Only cuts every
	// node it does not keep from the plan, for a run no host schedules.
	StartAt, StopAt, Only string
	// CheckArgs, when set, refuses the plan given the arguments it plans
	// with, describe's defaults filled in.
	CheckArgs    func(map[string]string) error
	CheckSecrets func(context.Context, []string) error
}

// PlannedRun contains validated SDK declarations and the host facts used to plan them.
type PlannedRun struct {
	Secrets  []string
	Requires []string
	Doc      nodeproto.PlanDoc
	JSON     []byte
	Args     map[string]string
}

// PlanDocument plans a pipeline in a short-lived SDK process, as a hosted
// run's plan process does, and validates the plan the same way.
func PlanDocument(ctx context.Context, cfg SessionConfig, req PlanRequest) (PlannedRun, error) {
	return planDocument(ctx, cfg, req, nil)
}

func planDocument(ctx context.Context, cfg SessionConfig, req PlanRequest, beforePlan func(map[string]string) error) (PlannedRun, error) {
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
	transport, output, err := scfg.protocol(nil)
	if err != nil {
		return PlannedRun{}, err
	}
	sess, err := StartProtocolSession(ctx, transport)
	if err == nil {
		err = scfg.hello(ctx, sess)
	}
	if err != nil {
		if sess != nil {
			err = errors.Join(err, sess.Close())
		} else {
			output.close()
		}
		return PlannedRun{}, err
	}
	stop := context.AfterFunc(ctx, func() { sess.failProtocol(context.Cause(ctx)) })
	defer stop()
	var out PlannedRun
	description, known, err := describeArgs(ctx, sess, scfg, req.Entrypoint, req.Args, req.Masker)
	out.Args = description.Args
	out.Secrets = describedSecrets(description.Description, false)
	out.Requires = slices.Clone(req.Requires)
	if err == nil && req.CheckArgs != nil {
		err = req.CheckArgs(out.Args)
	}
	if err == nil && req.CheckSecrets != nil {
		err = req.CheckSecrets(ctx, describedSecrets(description.Description, true))
	}
	err = releaseDescribed(output, known, err)
	if err == nil && beforePlan != nil {
		err = beforePlan(out.Args)
	}
	if err == nil {
		out.Doc, out.JSON, err = planRun(ctx, sess, scfg.Validation, req.Entrypoint, out.Args, req.Run)
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
	cerr := sess.Close()
	var perr *apierr.Error
	if err == nil && errors.As(cerr, &perr) {
		// safety: a process that broke the protocol after its reply cannot vouch for that plan.
		return PlannedRun{}, maskErr(req.Masker, cerr)
	}
	return out, maskErr(req.Masker, err)
}
