package hosthelpers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/hosthelpers/docker"
	"github.com/sparkwing-dev/sparkwing/internal/hosthelpers/git"
	helperruntime "github.com/sparkwing-dev/sparkwing/internal/hosthelpers/runtime"
	"github.com/sparkwing-dev/sparkwing/internal/hosthelpers/services"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// Run executes an operation for the current bound process.
func Run(ctx context.Context, req nodeproto.HelperRequest) nodeproto.HelperResponse {
	if !filepath.IsAbs(req.Dir) {
		return failed(errors.New("helper working directory must be absolute"))
	}
	ctx = helperruntime.WithEnvironment(ctx, req.Dir, req.Env)
	var args []json.RawMessage
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return failed(err)
	}
	var result any
	var err error
	switch req.Operation {
	case "services.Start":
		var specs []services.Service
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		if err = json.Unmarshal(args[0], &specs); err == nil {
			result, err = services.Start(ctx, specs)
		}
	case "services.Stop":
		var addrs []services.Addr
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		if err = json.Unmarshal(args[0], &addrs); err == nil {
			err = services.Stop(ctx, addrs)
		}

	case "git.Clone":
		if len(args) != 3 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		var a2 int
		if e := json.Unmarshal(args[2], &a2); e != nil {
			return failed(e)
		}
		err = git.Clone(ctx, a0, a1, git.WithDepth(a2))
	case "git.Fetch":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		err = git.Fetch(ctx, a0)
	case "git.ShortCommit":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.ShortCommit(ctx, a0)
	case "git.CurrentSHA":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.CurrentSHA(ctx, a0)
	case "git.CurrentBranch":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.CurrentBranch(ctx, a0)
	case "git.DefaultBranch":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.DefaultBranch(ctx, a0)
	case "git.RemoteOriginURL":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.RemoteOriginURL(ctx, a0)
	case "git.IsDirty":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.IsDirty(ctx, a0)
	case "git.FilesetHash":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.FilesetHash(ctx, a0)
	case "git.ChangedFiles":
		if len(args) != 2 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		result, err = git.ChangedFiles(ctx, a0, a1)
	case "git.TagsAtHead":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = git.TagsAtHead(ctx, a0)
	case "git.LatestTag":
		if len(args) != 2 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		result, err = git.LatestTag(ctx, a0, a1)
	case "git.TagExistsOnRemote":
		if len(args) != 2 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		result, err = git.TagExistsOnRemote(ctx, a0, a1)
	case "git.PushTag":
		if len(args) != 3 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		var a2 string
		if e := json.Unmarshal(args[2], &a2); e != nil {
			return failed(e)
		}
		err = git.PushTag(ctx, a0, a1, a2)
	case "docker.Build":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 docker.BuildConfig
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = docker.Build(ctx, a0)
	case "docker.BuildAndPush":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 docker.BuildConfig
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = docker.BuildAndPush(ctx, a0)
	case "docker.Push":
		if len(args) != 3 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 []string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		var a2 []string
		if e := json.Unmarshal(args[2], &a2); e != nil {
			return failed(e)
		}
		err = docker.Push(ctx, a0, a1, a2)
	case "docker.Login":
		if len(args) != 3 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var a1 string
		if e := json.Unmarshal(args[1], &a1); e != nil {
			return failed(e)
		}
		var a2 string
		if e := json.Unmarshal(args[2], &a2); e != nil {
			return failed(e)
		}
		err = docker.Login(ctx, a0, a1, a2)
	case "docker.BuildxPlatforms":
		if len(args) != 0 {
			return failed(errors.New("invalid helper argument count"))
		}
		result, err = docker.BuildxPlatforms(ctx)
	case "docker.FilterBuildxPlatforms":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 []string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = docker.FilterBuildxPlatforms(ctx, a0)
	case "docker.Run":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 docker.RunOptions
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		var stdout, stderr bytes.Buffer
		if a0.CaptureOutput {
			a0.Stdout, a0.Stderr = &stdout, &stderr
		} else {
			a0.Stdout, a0.Stderr = helperruntime.Output(ctx)
			if a0.Stdout == nil || a0.Stderr == nil {
				return failed(errors.New("host helper output unavailable"))
			}
		}
		err = docker.Run(ctx, a0)
		result = map[string]string{"stdout": stdout.String(), "stderr": stderr.String()}
	case "docker.ComputeTags":
		if len(args) != 0 {
			return failed(errors.New("invalid helper argument count"))
		}
		result, err = docker.ComputeTags(ctx)
	case "docker.ComputeTagsIn":
		if len(args) != 1 {
			return failed(errors.New("invalid helper argument count"))
		}
		var a0 string
		if e := json.Unmarshal(args[0], &a0); e != nil {
			return failed(e)
		}
		result, err = docker.ComputeTagsIn(ctx, a0)
	default:
		return failed(fmt.Errorf("unknown host helper %q", req.Operation))
	}
	raw, encodeErr := sdkserve.EncodeServiceRequest(result)
	if encodeErr != nil {
		return failed(encodeErr)
	}
	response := nodeproto.HelperResponse{Result: raw}
	if err != nil {
		response = failed(err)
		response.Result = raw
	}
	return response
}

func failed(err error) nodeproto.HelperResponse {
	code := ""
	for _, class := range []error{git.ErrTagAlreadyExists, docker.ErrDockerUnavailable, docker.ErrBuildxRequired, docker.ErrPlatformUnsupported} {
		if errors.Is(err, class) {
			code = class.Error()
			break
		}
	}
	return nodeproto.HelperResponse{Error: err.Error(), Code: code, Result: json.RawMessage("null")}
}
