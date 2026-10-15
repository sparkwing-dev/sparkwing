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
	Checkout      runspec.CheckoutPolicy
	Engine        *engine.Identity
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

// EngineResolver selects a verified engine or restores the accepted engine identity.
// engine.Resolver implements this contract for every placement.
type EngineResolver interface {
	Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error)
	Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error)
}

// Options supplies host-owned workspace and authority, separate from sealed source data.
type Options struct {
	Workspace string
	Access    Access
	Engines   EngineResolver
	Target    releaseasset.Target
	SDK       engine.SDKKind
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

// Entry is the acquired checkout and SDK command. Command excludes protocol argv.
// Go uses its locally built or provenance-checked binary; TypeScript uses node
// and the main file, without a bootstrap preload.
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
}

// Acquire clones the sealed SHA exactly or uses the authorized local checkout.
// A source bundle is only a seed: checkout still verifies the requested commit.
// Entry installation receives only procenv.Install's base, never a capability.
// After checkout, ReadPin and Engines.Resolve select the exact verified engine before SDK
// build/install, describe or plan. That executable owns all SDK build/install and host execution.
// A supplied sealed Engine uses Engines.Ensure and must agree with the checkout pin. The result's
// executable hosts the SDK command on every placement, including TypeScript on Windows.
func Acquire(ctx context.Context, spec Spec, options Options) (*Entry, error) {
	return acquire(ctx, spec, options)
}

// Close releases acquisition-owned resources without removing a user's checkout.
func (e *Entry) Close() error { return e.close() }

// ForRequest uses the captured intake checkout policy for planning acquisition.
// Code and checkout-derived policy can resolve only at AcceptPlan.
// Source.Location supplies URL/local path/pipeline directory. Repo display is never a clone URL.
func ForRequest(request runspec.SealedRequest) (Spec, error) {
	return requestSource(request)
}

// ForExecution uses the accepted checkout winner and immutable acquisition identity.
// Executors never inspect raw PlanDoc.Source or a host's machine preferences.
// The projection also carries the accepted portable engine identity.
// It preserves the first seal's source location, never the executor's ambient checkout.
func ForExecution(spec runspec.Spec) (Spec, error) {
	return executionSource(spec)
}
