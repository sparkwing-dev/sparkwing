// Package source acquires the checkout and entry shared by every host placement.
package source

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Spec carries the sealed source identity, commit and checkout policy.
type Spec struct {
	RepoKey           string
	RemoteURL         string
	SHA               string
	PipelineSHA       string
	CommittedCheckout bool
	DefaultBranch     string
	LocalCheckout     string
	PipelineDir       string
	Checkout          runspec.CheckoutPolicy
	Engine            *engineid.Identity
	NoBincache        bool
	HashAllFiles      bool
	// BundleDigest identifies an optional authorized source_bundle seed.
	BundleDigest string
}

// Access supplies host-owned source authority without environment credentials.
type Access interface {
	GitCredential(context.Context, Spec) (Credential, error)
	BundleURL(context.Context, string) (string, error)
}

// Credential retains source authority until git uses its private credential files.
type Credential struct {
	Kind              string
	Host              string
	Username          string
	Secret            string
	KnownHosts        string
	ExtraRepositories []string
}

// EngineResolver selects or restores an exact verified engine.
type EngineResolver interface {
	Resolve(context.Context, engineid.Pin, releaseasset.Target) (engine.Resolved, error)
	Ensure(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error)
}

// Options supplies host-owned workspace and authority separately from sealed source data.
type Options struct {
	PreparedCheckout string
	LocalBundle      string
	Workspace        string
	Access           Access
	Engines          EngineResolver
	Target           releaseasset.Target
	SDK              engineid.SDKKind
	// ReadPin defaults to engine.ReadPin and must not execute repository code.
	ReadPin func(string, engineid.SDKKind) (engineid.Pin, error)
	// InstallSDK defaults to the verified selected engine's InstallSDK method.
	InstallSDK func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error)
	// BuildEnvironment defaults to procenv.Build; adapters may supply the shared policy implementation.
	BuildEnvironment func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error)
	// BundleTransport supplies signed-object transfers; the source client always refuses redirects.
	// Nil uses the standard HTTP transport.
	BundleTransport http.RoundTripper
	// ResolveRef reads an exact qualified ref through host authority. An absent ref
	// returns an empty commit and nil error; nil uses Git with Access.
	ResolveRef func(context.Context, string) (string, error)
	// ReadFile reads one committed file through host authority; nil fetches it with Access.
	ReadFile  func(ctx context.Context, sha, name string) ([]byte, bool, error)
	Base      procenv.Base
	GitAccess GitAccess
}

// GitAccess selects whose credentials fetch remote source; hosts derive it from authenticated placement.
type GitAccess uint8

const (
	// GitAccessTeam fetches with the controller-released team or App credential through pinned hosts.
	GitAccessTeam GitAccess = iota
	// GitAccessOwner fetches with the host owner's own Git configuration and never requests team credentials.
	GitAccessOwner
)

func (o Options) ownerGit() bool { return o.GitAccess == GitAccessOwner }

// Entry contains the acquired checkout and SDK command without protocol arguments.
type Entry struct {
	Engine    engine.Resolved
	Directory string
	Command   []string
	RepoKey   string
	SHA       string
	// DefaultBranch is resolved from origin HEAD only when the sealed request lacks it.
	DefaultBranch     string
	ownedDirectory    string
	pipelineCheckout  string
	pipelineEntry     *Entry
	checkoutDirectory string
	releaseSDK        func() error
}

// CheckoutDirectory returns the acquired root that bounds checkout-derived file reads.
func (e *Entry) CheckoutDirectory() string { return e.checkoutDirectory }

// PipelineSource projects the sealed code commit without widening acquisition authority.
func (s Spec) PipelineSource() Spec {
	s.SHA, s.PipelineSHA, s.CommittedCheckout = s.PipelineSHA, "", true
	return s
}
