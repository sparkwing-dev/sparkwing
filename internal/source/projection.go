package source

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func requestSource(sealed runspec.SealedRequest) (Spec, error) {
	r := sealed.Request
	if !r.Options.RepoLocation.Set || r.Options.RepoLocation.Value == "" {
		return Spec{}, errors.New("sealed request has no source location; repository display is not acquisition authority")
	}
	spec := Spec{RepoKey: string(r.Git.Repository.Key), SHA: r.Git.SHA, PipelineDir: r.Source.Entrypoint}
	location := r.Options.RepoLocation.Value
	if filepath.IsAbs(location) {
		spec.LocalCheckout = filepath.Clean(location)
	} else {
		remote, err := sourceurl.ValidateCloneURL(location)
		if err != nil {
			return Spec{}, fmt.Errorf("sealed source location: %w", err)
		}
		spec.RemoteURL = remote
	}
	if r.Git.DefaultBranch.Set {
		if r.Git.DefaultBranch.Value == "" {
			return Spec{}, errors.New("sealed default branch is explicitly empty")
		}
		spec.DefaultBranch = r.Git.DefaultBranch.Value
	}
	if r.Source.BundleDigest.Set {
		spec.BundleDigest = r.Source.BundleDigest.Value
	}
	if r.Options.Checkout.Set {
		spec.Checkout = clonePolicy(r.Options.Checkout.Value)
	}
	return spec, validateSpec(spec)
}

func executionSource(sealed runspec.Spec) (Spec, error) {
	spec, err := requestSource(sealed.Request)
	if err != nil {
		return Spec{}, err
	}
	spec.Checkout = runspec.CheckoutPolicy{}
	if winner := sealed.Resolved.Settings.Options.Checkout; winner.Set {
		spec.Checkout = clonePolicy(winner.Value)
	}
	if sealed.Resolved.Entrypoint != "" {
		spec.PipelineDir = sealed.Resolved.Entrypoint
	}
	if spec.DefaultBranch == "" && sealed.Resolved.DefaultBranch.Set {
		spec.DefaultBranch = sealed.Resolved.DefaultBranch.Value
	}
	identity := sealed.Resolved.Engine
	spec.Engine = &identity
	return spec, validateSpec(spec)
}

func clonePolicy(policy runspec.CheckoutPolicy) runspec.CheckoutPolicy {
	policy.Origins = maps.Clone(policy.Origins)
	policy.Policy.FullHistory = cloneValue(policy.Policy.FullHistory)
	policy.Policy.Depth = cloneValue(policy.Policy.Depth)
	policy.Policy.Tags = cloneValue(policy.Policy.Tags)
	policy.Policy.Submodules = cloneValue(policy.Policy.Submodules)
	policy.Policy.LFS = cloneValue(policy.Policy.LFS)
	return policy
}

func cloneValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}
