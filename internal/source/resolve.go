package source

import (
	"context"
	"errors"
	"os/exec"
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
	client := &gitClient{owner: options.ownerGit(), env: gitEnvironment(env), workspace: options.Workspace, masker: options.Base.Masker}
	if spec.LocalCheckout != "" {
		return resolveReference(ref, true, func(refs []string) (map[string]string, error) {
			commits := make(map[string]string)
			for _, candidate := range refs {
				sha, err := client.run(ctx, spec.LocalCheckout, "", "rev-parse", "--verify", "--quiet", "--end-of-options", candidate+"^{commit}")
				if err != nil {
					var exit *exec.ExitError
					if errors.As(err, &exit) && exit.ExitCode() == 1 {
						continue
					}
					return nil, err
				}
				commits[candidate] = sha
			}
			return commits, nil
		})
	}
	remote := spec.RemoteURL
	if options.Access != nil {
		remote, err = client.authorizeRemote(ctx, spec, remote, options.Access)
		if err != nil {
			return "", err
		}
	}
	return resolveReference(ref, false, func(refs []string) (map[string]string, error) {
		return resolveRemoteRefs(ctx, client, remote, refs)
	})
}

func normalizeRef(ref string) (string, bool) {
	for _, prefix := range []string{"origin/", "refs/remotes/origin/"} {
		if branch, ok := strings.CutPrefix(ref, prefix); ok {
			return "refs/heads/" + branch, true
		}
	}
	return ref, false
}

func resolveReference(ref string, local bool, lookup func([]string) (map[string]string, error)) (string, error) {
	if !validRefName(ref) {
		return "", errors.New("source revision is not a valid ref")
	}
	ref, origin := normalizeRef(ref)
	if local && origin {
		ref = "refs/remotes/origin/" + strings.TrimPrefix(ref, "refs/heads/")
	}
	refs := []string{ref}
	if ref != "HEAD" && !runspec.IsCommitID(ref) && !strings.HasPrefix(ref, "refs/") {
		refs = []string{"refs/heads/" + ref, "refs/tags/" + ref}
	}
	commits, err := lookup(refs)
	if err != nil {
		return "", err
	}
	selected := ""
	for _, candidate := range refs {
		sha := commits[candidate]
		if sha == "" {
			continue
		}
		if !runspec.IsCommitID(sha) {
			return "", errors.New("source ref returned an invalid commit")
		}
		if selected != "" && selected != sha {
			return "", errors.New("source revision is ambiguous")
		}
		selected = sha
	}
	if selected == "" {
		return "", errors.New("source revision is unavailable")
	}
	return selected, nil
}

func validRefName(ref string) bool {
	if ref == "" || ref == "@" || strings.HasPrefix(ref, "-") || strings.HasSuffix(ref, ".") || strings.ContainsAny(ref, "~^:?*[\\") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	for _, r := range ref {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func resolveRemoteRefs(ctx context.Context, client *gitClient, remote string, refs []string) (map[string]string, error) {
	args := []string{"ls-remote", "--", remote}
	args = append(args, refs...)
	for _, candidate := range refs {
		if strings.HasPrefix(candidate, "refs/tags/") {
			args = append(args, candidate+"^{}")
		}
	}
	out, err := client.run(ctx, "", remote, args...)
	if err != nil {
		return nil, err
	}
	commits := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		sha, name, ok := strings.Cut(line, "\t")
		if !ok || !runspec.IsCommitID(sha) {
			return nil, errors.New("source ref returned an invalid commit")
		}
		commits[name] = sha
	}
	for _, name := range refs {
		if peeled := commits[name+"^{}"]; peeled != "" {
			commits[name] = peeled
		}
	}
	return commits, nil
}
