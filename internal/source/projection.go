package source

import (
	"errors"
	"maps"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// ForRequest projects the sealed source location and intake checkout policy.
func ForRequest(sealed runspec.SealedRequest) (Spec, error) {
	r := sealed.Request
	location := r.Source.Location
	if (location.RemoteURL == "") == (location.LocalCheckout == "") {
		return Spec{}, errors.New("sealed source requires exactly one acquisition location")
	}
	if !filepath.IsLocal(location.PipelineDir) {
		return Spec{}, errors.New("sealed pipeline directory must stay inside the checkout")
	}
	spec := Spec{RepoKey: string(r.Git.Repository.Key), SHA: r.Git.SHA, PipelineDir: location.PipelineDir, NoBincache: r.Source.NoBincache, HashAllFiles: r.Source.HashAllFiles}
	if r.Source.WorkingTree && location.LocalCheckout == "" {
		return Spec{}, errors.New("working-tree source requires a local checkout")
	}
	if !r.Source.WorkingTree {
		spec.PipelineSHA = r.Source.Ref
		if r.Source.Ref != "" && r.Source.Ref == r.Git.Ref {
			spec.PipelineSHA = r.Git.SHA
		}
	}
	if location.LocalCheckout != "" {
		spec.CommittedCheckout = r.Source.CommittedCheckout
		if !filepath.IsAbs(location.LocalCheckout) {
			return Spec{}, errors.New("sealed local checkout must be absolute")
		}
		spec.LocalCheckout = filepath.Clean(location.LocalCheckout)
	} else {
		remote, err := sourceurl.ValidateCloneURL(location.RemoteURL)
		if err != nil {
			return Spec{}, errors.New("sealed remote acquisition location is invalid")
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

// ForExecution projects the accepted checkout policy and engine identity.
func ForExecution(sealed runspec.Spec) (Spec, error) {
	spec, err := ForRequest(sealed.Request)
	if err != nil {
		return Spec{}, err
	}
	spec.Checkout = runspec.CheckoutPolicy{}
	if winner := sealed.Resolved.Settings.Options.Checkout; winner.Set {
		spec.Checkout = clonePolicy(winner.Value)
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
