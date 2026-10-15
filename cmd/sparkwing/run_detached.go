package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const detachedPath = "run --sw-detached"

type submitResult struct {
	orchestrator.RunHandle
	AlreadySubmitted bool   `json:"already_submitted,omitempty"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
}

func runDetached(ctx context.Context, pipeline string, flags runFlags, passthrough []string) error {
	if flags.workers != 0 {
		return &apierr.Error{Code: apierr.InvalidArgs, Message: "--sw-workers requires foreground execution"}
	}
	format, err := resolveDetachedOutput(flags.outputFormat)
	if err != nil {
		return err
	}
	if flags.ref != "" || flags.pipelineRef != "" {
		return &apierr.Error{Code: apierr.PolicyMissing, Message: "detached source selection requires a sealed source checkout binding"}
	}
	directory, declared, err := resolveSubmitRepo(ctx, pipeline, flags.pipelineRef)
	if err != nil {
		return err
	}
	selectedSource, err := projectconfig.PipelineSource(directory, pipeline)
	if err != nil {
		return err
	}
	if selectedSource != "" {
		return &apierr.Error{Code: apierr.PolicyMissing, Message: "detached pipeline source requires a sealed source checkout binding"}
	}
	identity, err := pipelineIdentityAt(filepath.Join(directory, ".sparkwing"), pipeline)
	if err != nil {
		return err
	}
	if declared == nil {
		declared, err = repos.DescribeRepo(directory)
		if err != nil {
			return err
		}
	}
	schema, err := boundPipelineSchema(declared, identity)
	if err != nil {
		return err
	}
	args, err := parsePipelineArgs(schema, passthrough)
	if err != nil {
		return err
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(paths.Root, "captured-environments"))
	if err != nil {
		return err
	}
	priority, err := localHostPriority(ctx, flags.priority, func(ctx context.Context) (wingwire.QueueState, error) {
		return wingdclient.Query(ctx, wingdclient.Options{Version: installedVersion(), NoTakeover: true, Spawn: wingdclient.NoHostSpawn})
	})
	if err != nil {
		return err
	}
	request, err := prepareLocalHostRequest(ctx, directory, nodehost.PipelineIdentity{Invoked: identity.Name, Entrypoint: identity.Entrypoint}, hostedRun{schema: schema, args: args, flags: flags, env: os.Environ(), priority: priority, logFormat: format}, snapshots)
	if err != nil {
		return err
	}
	request.IdempotencyKey = strings.TrimSpace(flags.idempotencyKey)
	controller, closeTransport, err := resolveEngineController(ctx, "")
	if err != nil {
		return err
	}
	defer closeTransport()
	result, err := submitDetached(ctx, controller, request, flags.runHandleFile, flags.requestID)
	if err != nil {
		return err
	}
	return emitSubmitResult(result, format)
}

func submitDetached(ctx context.Context, controller controllerwire.Controller, request controllerwire.CreateRunRequest, handle, requestID string) (result submitResult, err error) {
	masks := secrets.NewMasker()
	for _, value := range request.Request.RequestedArgs {
		masks.Register(value)
	}
	defer func() {
		if err != nil {
			err = &maskedCLIError{err: err, masker: masks}
		}
	}()
	if controller == nil {
		return result, &apierr.Error{Code: apierr.PolicyMissing, Message: "detached intake requires the local controller"}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if handle != "" {
		release, err := orchestrator.ReserveRunHandle(handle)
		if err != nil {
			return result, err
		}
		defer release()
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = rand.Text()
	}
	response, err := controller.CreateRun(ctx, request)
	masks.Register(response.HereBearer)
	if err != nil {
		return result, err
	}
	if _, err := initialTriggerReceipt(request.Request.Pipeline, false, response); err != nil {
		return result, err
	}
	status := "pending"
	if response.Replayed {
		detail, err := controller.ReadRun(ctx, response.RunID)
		if err != nil {
			return result, err
		}
		if detail.ID != response.RunID || detail.Status == "" {
			return result, &apierr.Error{Code: apierr.ProtocolError, Message: "detached replay returned no matching run status"}
		}
		status = detail.Status
	}
	result = submitResult{RunHandle: orchestrator.NewRunHandle(response.RunID, request.Request.Pipeline, "", status), AlreadySubmitted: response.Replayed, IdempotencyKey: request.IdempotencyKey, RequestID: requestID}
	if handle != "" {
		if err := orchestrator.PublishRunHandle(handle, result.RunHandle); err != nil {
			return result, err
		}
	}
	return result, nil
}

func resolveDetachedOutput(value string) (string, error) {
	switch value {
	case "", "pretty", "json", "plain":
	default:
		return "", fmt.Errorf("%s: --sw-output must be pretty, json or plain", detachedPath)
	}
	return resolveTTYAwareOutput(value, detachedPath)
}

func refuseDetachedOnlyFlags(flags runFlags) error {
	for _, flag := range flags.detachedOnlyFlags() {
		if flag.value != "" {
			return fmt.Errorf("run: %s requires --sw-detached", flag.name)
		}
	}
	return nil
}

func resolveSubmitRepo(ctx context.Context, pipeline, pipelineRef string) (string, []nodeproto.PipelineDescription, error) {
	start := mustGetwd()
	if pipelineRef == "" {
		if dir, err := findSparkwingDirFrom(start); err == nil {
			source, err := projectconfig.PipelineSource(filepath.Dir(dir), pipeline)
			if err != nil {
				return "", nil, err
			}
			pipelineRef = source
		}
	}
	if pipelineRef != "" {
		dir, err := gitOutput(ctx, start, nil, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", nil, err
		}
		dir = strings.TrimSpace(dir)
		if info, err := os.Stat(filepath.Join(dir, ".sparkwing", "sparkwing.yaml")); err != nil || info.IsDir() {
			return "", nil, fmt.Errorf("execution checkout %s requires .sparkwing/sparkwing.yaml", dir)
		}
		return dir, nil, nil
	}
	if dir, declared, ok := localRepoDeclaring(start, pipeline); ok {
		return dir, declared, nil
	}

	path, err := repos.ResolveRepoForPipelineCached(pipeline)
	if err == nil {
		return path, nil, nil
	}
	if errors.Is(err, repos.ErrNotFound) {
		return "", nil, fmt.Errorf(
			"run --sw-detached: no project here or in the repo registry declares a pipeline named %q.\n"+
				"Run it from the checkout that defines it, pass -C <path> to point at that checkout, "+
				"or register it with `sparkwing repos add <path>`.\n"+
				"A registered checkout whose pipeline binary has never been built is not searched; "+
				"run `sparkwing pipeline list` there once first", pipeline)
	}
	return "", nil, fmt.Errorf("run --sw-detached: resolve %q: %w", pipeline, err)
}

func localRepoDeclaring(start, pipeline string) (string, []nodeproto.PipelineDescription, bool) {
	directory, err := findSparkwingDirFrom(start)
	if err != nil {
		return "", nil, false
	}
	identity, err := pipelineIdentityAt(directory, pipeline)
	if err != nil {
		return "", nil, false
	}
	declared, err := repos.DescribeRepo(filepath.Dir(directory))
	if err != nil {
		return "", nil, false
	}
	if _, err := boundPipelineSchema(declared, identity); err != nil {
		return "", nil, false
	}
	return filepath.Dir(directory), declared, true
}

func renderArgs(args map[string]string) string {
	if len(args) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("--%s=%s", key, args[key]))
	}
	return strings.Join(parts, " ")
}

func emitSubmitResult(result submitResult, format string) error {
	if result.RunID == "" {
		return errors.New("detached intake returned no run identity")
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(result)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, result.RunID)
		return err
	default:
		verb := "submitted"
		if result.AlreadySubmitted {
			verb = "already submitted"
		}
		_, err := fmt.Fprintf(os.Stdout, "run %s %s (%s), status %s\n  follow: sparkwing runs logs --run %s --follow\n  cancel: sparkwing runs cancel --run %s\n", result.RunID, verb, result.Pipeline, result.Status, result.RunID, result.RunID)
		return err
	}
}
