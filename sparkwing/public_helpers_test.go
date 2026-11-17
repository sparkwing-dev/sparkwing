package sparkwing_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
	"github.com/sparkwing-dev/sparkwing/sparkwing/git"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
	"github.com/sparkwing-dev/sparkwing/sparkwing/services"
)

type relayHost struct {
	sdkserve.Services
	request nodeproto.HelperRequest
	result  any
	err     error
}

func (h *relayHost) Helper(_ context.Context, req nodeproto.HelperRequest, out any) error {
	h.request = req
	if h.err != nil {
		return h.err
	}
	if out != nil {
		data, err := json.Marshal(h.result)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

func TestPublicGitHelperRelays(t *testing.T) {
	host := &relayHost{Services: sdkserve.ServicesFrom(t.Context())}
	ctx := sdkserve.WithServices(t.Context(), host)
	for _, tc := range []struct {
		name   string
		args   string
		result any
		call   func() (any, error)
	}{
		{"Clone", `["remote","dest",2]`, nil, func() (any, error) {
			opt := git.WithDepth(2)
			return nil, git.Clone(ctx, "remote", "dest", opt)
		}},
		{"Fetch", `["repo"]`, nil, func() (any, error) { return nil, git.Fetch(ctx, "repo") }},
		{"ShortCommit", `["repo"]`, "abc", func() (any, error) { return git.ShortCommit(ctx, "repo") }},
		{"CurrentSHA", `["repo"]`, "abcdef", func() (any, error) { return git.CurrentSHA(ctx, "repo") }},
		{"CurrentBranch", `["repo"]`, "feature", func() (any, error) { return git.CurrentBranch(ctx, "repo") }},
		{"DefaultBranch", `["repo"]`, "main", func() (any, error) { return git.DefaultBranch(ctx, "repo") }},
		{"RemoteOriginURL", `["repo"]`, "remote", func() (any, error) { return git.RemoteOriginURL(ctx, "repo") }},
		{"IsDirty", `["repo"]`, true, func() (any, error) { return git.IsDirty(ctx, "repo") }},
		{"TagsAtHead", `["repo"]`, []string{"v1"}, func() (any, error) { return git.TagsAtHead(ctx, "repo") }},
		{"ChangedFiles", `["repo","base"]`, []string{"file"}, func() (any, error) { return git.ChangedFiles(ctx, "repo", "base") }},
		{"LatestTag", `["repo","v"]`, "v1", func() (any, error) { return git.LatestTag(ctx, "repo", "v") }},
		{"TagExistsOnRemote", `["repo","v1"]`, true, func() (any, error) { return git.TagExistsOnRemote(ctx, "repo", "v1") }},
		{"PushTag", `["repo","v1","release"]`, nil, func() (any, error) { return nil, git.PushTag(ctx, "repo", "v1", "release") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host.result = tc.result
			got, err := tc.call()
			if err != nil || !reflect.DeepEqual(got, tc.result) || host.request.Operation != "git."+tc.name || string(host.request.Args) != tc.args {
				t.Fatalf("result=%v operation=%s args=%s error=%v", got, host.request.Operation, host.request.Args, err)
			}
			host.err = errors.New("host failure")
			_, err = tc.call()
			if !errors.Is(err, host.err) {
				t.Fatalf("lost host error: %v", err)
			}
			host.err = nil
		})
	}
}

func TestPublicDockerHelperRelays(t *testing.T) {
	host := &relayHost{Services: sdkserve.ServicesFrom(t.Context())}
	ctx := sdkserve.WithServices(t.Context(), host)
	result := docker.BuildResult{Image: "registry/image", Registries: []string{"registry"}, Digests: map[string]string{"registry/image": "sha256:value"}}
	cfg := docker.BuildConfig{Image: "image"}
	for _, tc := range []struct {
		name   string
		args   any
		result any
		call   func() (any, error)
	}{
		{"BuildAndPush", []any{cfg}, result, func() (any, error) { return docker.BuildAndPush(ctx, cfg) }},
		{"Push", []any{"image", []string{"tag"}, []string{"registry"}}, nil, func() (any, error) { return nil, docker.Push(ctx, "image", []string{"tag"}, []string{"registry"}) }},
		{"Login", []any{"registry", "user", "secret"}, nil, func() (any, error) { return nil, docker.Login(ctx, "registry", "user", "secret") }},
		{"BuildxPlatforms", []any{}, []string{"linux/amd64"}, func() (any, error) { return docker.BuildxPlatforms(ctx) }},
		{"FilterBuildxPlatforms", []any{[]string{"linux/amd64", "linux/arm64"}}, []string{"linux/amd64"}, func() (any, error) { return docker.FilterBuildxPlatforms(ctx, []string{"linux/amd64", "linux/arm64"}) }},
		{"ComputeTags", []any{}, docker.ImageTag{Commit: "abc", Content: "def"}, func() (any, error) { return docker.ComputeTags(ctx) }},
		{"ComputeTagsIn", []any{"repo"}, docker.ImageTag{Commit: "abc", Content: "def"}, func() (any, error) { return docker.ComputeTagsIn(ctx, "repo") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host.result = tc.result
			wantArgs, err := sdkserve.EncodeServiceRequest(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.call()
			if err != nil || !reflect.DeepEqual(got, tc.result) || host.request.Operation != "docker."+tc.name || string(host.request.Args) != string(wantArgs) {
				t.Fatalf("result=%v operation=%s args=%s error=%v", got, host.request.Operation, host.request.Args, err)
			}
			host.err = errors.New("host failure")
			_, err = tc.call()
			if !errors.Is(err, host.err) {
				t.Fatalf("lost host error: %v", err)
			}
			host.err = nil
		})
	}
	tag := docker.ImageTag{Commit: "abc", Content: "def", Dirty: true}
	if tag.ProdTag() != "commit-abc-files-def-dirty-prod" || !reflect.DeepEqual(tag.All(), []string{"commit-abc-files-def-dirty", "commit-abc-files-def-dirty-prod"}) {
		t.Fatalf("tags=%v", tag.All())
	}
}

func TestPublicMemoRecipeOptions(t *testing.T) {
	option := inputs.Ignore("vendor/", "**/*.tmp")
	recipe := inputs.RepoFiles(option)
	if recipe.Kind != "repo_files" || !reflect.DeepEqual(recipe.Patterns, []string{"vendor/", "**/*.tmp"}) {
		t.Fatalf("recipe=%+v", recipe)
	}
	if literal := inputs.Const("version"); literal.Kind != "const" || literal.Value != "version" {
		t.Fatalf("recipe=%+v", literal)
	}
}

func TestPublicServicesCallbackAndCleanup(t *testing.T) {
	host := &relayHost{Services: sdkserve.ServicesFrom(t.Context()), result: []any{map[string]any{"Name": "db", "hostPort": 1234, "cleanup_id": "owned"}}}
	ctx := sdkserve.WithServices(t.Context(), host)
	failure := errors.New("body failed")
	called := false
	err := services.WithServices(ctx, []services.Service{{Image: "postgres", Name: "db"}}, func(context.Context) error {
		called = true
		if host.request.Operation != "services.Start" {
			t.Fatal(host.request.Operation)
		}
		return failure
	})
	if !called || !errors.Is(err, failure) || host.request.Operation != "services.Stop" {
		t.Fatalf("called=%v operation=%s args=%s error=%v", called, host.request.Operation, host.request.Args, err)
	}
}
