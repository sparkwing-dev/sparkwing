// Package source acquires the checkout and entry shared by every host placement.
package source

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Spec carries the sealed source identity, commit and checkout policy.
type Spec struct {
	RepoKey       string
	RemoteURL     string
	SHA           string
	DefaultBranch string
	LocalCheckout string
	PipelineDir   string
	Checkout      runspec.CheckoutPolicy
	Engine        *engine.Identity
	// BundleDigest identifies an optional authorized source_bundle seed.
	BundleDigest string
}

// Access supplies live, audited source authority without environment credentials.
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
	Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error)
	Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error)
}

// Options supplies host-owned workspace and authority separately from sealed source data.
type Options struct {
	PreparedCheckout string
	Workspace        string
	Access           Access
	Engines          EngineResolver
	Target           releaseasset.Target
	SDK              engine.SDKKind
	// ReadPin defaults to engine.ReadPin and must not execute repository code.
	ReadPin func(string, engine.SDKKind) (engine.Pin, error)
	// InstallSDK defaults to the verified selected engine's InstallSDK method.
	InstallSDK func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error)
	// BuildEnvironment defaults to procenv.Build; adapters may supply the shared policy implementation.
	BuildEnvironment func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
	// BundleTransport supplies signed-object transfers; the source client always refuses redirects.
	// Nil uses the standard HTTP transport.
	BundleTransport http.RoundTripper
	Base            procenv.Base
}

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
	checkoutDirectory string
	releaseSDK        func() error
}

// CheckoutDirectory returns the acquired root that bounds checkout-derived file reads.
func (e *Entry) CheckoutDirectory() string { return e.checkoutDirectory }
