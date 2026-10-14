// Package source acquires the checkout and entry shared by every host placement.
package source

import "context"

// Spec is the source-acquisition projection of the sealed request or run spec.
// Its identity and SHA come from intake, never from a later branch-head lookup.
// A local checkout may omit SHA before resolution; a remote checkout may not.
type Spec struct {
	RepoKey       string
	RemoteURL     string
	SHA           string
	DefaultBranch string
	LocalCheckout string
	PipelineDir   string
	Depth         *int
	FullHistory   bool
	Tags          bool
	Submodules    bool
	LFS           bool
	// BundleDigest identifies an optional authorized source_bundle seed.
	BundleDigest string
}

// Access supplies live, audited source authority without process-environment credentials.
// Team credential release remains the controller's decision. Local credential use
// must be fenced by the runner owner's repository allow-list.
type Access interface {
	GitCredential(context.Context, Spec) (Credential, error)
	BundleURL(context.Context, string) (string, error)
}

// Credential holds an ephemeral source credential in memory until a private file
// is needed by git. Files must use fssecure and be removed when git exits.
type Credential struct {
	Kind              string
	Host              string
	Username          string
	Secret            string
	KnownHosts        string
	ExtraRepositories []string
}

// Options supplies host-owned workspace and authority, separate from sealed source data.
type Options struct {
	Workspace string
	Access    Access
}

// Entry is the acquired checkout and SDK command. Command excludes protocol argv.
// Go uses its locally built or provenance-checked binary; TypeScript uses node
// and the main file, without a bootstrap preload.
type Entry struct {
	Directory string
	Command   []string
	RepoKey   string
	SHA       string
	// DefaultBranch is resolved from origin HEAD only when the sealed request lacks it.
	DefaultBranch string
}

// Acquire clones the sealed SHA exactly or uses the authorized local checkout.
// A source bundle is only a seed: checkout still verifies the requested commit.
// Entry installation receives only procenv.Install's base, never a capability.
func Acquire(ctx context.Context, spec Spec, options Options) (*Entry, error) {
	panic("unimplemented: c3-protocol-sdk")
}

// Close releases acquisition-owned resources without removing a user's checkout.
func (e *Entry) Close() error { panic("unimplemented: c3-protocol-sdk") }
