package source

import (
	"context"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ResolveRef looks up ref in the authorized acquisition location without selecting an SDK.
func ResolveRef(ctx context.Context, spec Spec, ref string, options Options) (string, error) {
	if (spec.LocalCheckout == "") == (spec.RemoteURL == "") {
		return "", errors.New("source resolution requires exactly one acquisition location")
	}
	env, err := sourceEnvironment(options, procenv.Git)
	if err != nil {
		return "", err
	}
	if spec.RemoteURL, err = tenantRemote(spec.RemoteURL, options); err != nil {
		return "", err
	}
	client := &gitClient{owner: options.ownerGit(), env: gitEnvironment(env), workspace: options.Workspace}
	ref, origin := normalizeRef(ref)
	if spec.LocalCheckout != "" {
		if origin {
			ref = "refs/remotes/origin/" + strings.TrimPrefix(ref, "refs/heads/")
		}
		return client.run(ctx, spec.LocalCheckout, "", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	}
	remote := spec.RemoteURL
	if options.Access != nil {
		remote, err = client.authorizeRemote(ctx, spec, remote, options.Access)
		if err != nil {
			return "", err
		}
	}
	return resolveRemoteRef(ctx, client, remote, ref)
}

func normalizeRef(ref string) (string, bool) {
	for _, prefix := range []string{"origin/", "refs/remotes/origin/"} {
		if branch, ok := strings.CutPrefix(ref, prefix); ok {
			return "refs/heads/" + branch, true
		}
	}
	return ref, false
}

func resolveRemoteRef(ctx context.Context, client *gitClient, remote, ref string) (string, error) {
	if ref != "HEAD" {
		if _, err := client.run(ctx, "", "", "check-ref-format", "--allow-onelevel", ref); err != nil {
			return "", errors.New("source revision is not a valid ref")
		}
	}
	refs := []string{ref}
	if ref != "HEAD" && !strings.HasPrefix(ref, "refs/") {
		refs = []string{"refs/heads/" + ref, "refs/tags/" + ref}
	}
	args := []string{"ls-remote", "--exit-code", "--", remote}
	args = append(args, refs...)
	for _, candidate := range refs {
		if strings.HasPrefix(candidate, "refs/tags/") {
			args = append(args, candidate+"^{}")
		}
	}
	out, err := client.run(ctx, "", remote, args...)
	if err != nil {
		return "", err
	}
	commits := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if !ok || !runspec.IsCommitID(sha) {
			return "", errors.New("source ref returned an invalid commit")
		}
		commits[name] = sha
	}
	selected := ""
	for _, name := range refs {
		sha := commits[name+"^{}"]
		if sha == "" {
			sha = commits[name]
		}
		if sha != "" {
			if selected != "" && selected != sha {
				return "", errors.New("source revision is ambiguous")
			}
			selected = sha
		}
	}
	if selected == "" {
		return "", errors.New("source revision is unavailable")
	}
	return selected, nil
}
