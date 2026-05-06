package sparkwing

// Venue is the pipeline-level declaration of where the pipeline is
// allowed to run. IMP-011: today every pipeline is implicitly
// dispatchable anywhere — `wing X` runs locally and `wing X --on
// prod` ships the trigger to a remote runner. Some pipelines aren't
// safe to dispatch remotely (cluster-up shells out to terraform / aws
// against the operator's laptop credentials; the cluster doesn't have
// those). Some pipelines, symmetrically, only make sense in-cluster
// (prune-old-pvcs, secret-rotation that touches in-cluster state).
//
// Authors opt in by implementing the optional Venue() method on the
// pipeline value:
//
//	func (ClusterUp) Venue() sparkwing.Venue { return sparkwing.VenueLocalOnly }
//
// The dispatcher checks this before sending the trigger and refuses
// `--on PROFILE` for LocalOnly pipelines / refuses bare invocation
// for ClusterOnly. Default is VenueEither (the zero value) so
// existing pipelines are unaffected.
type Venue int

const (
	// VenueEither is the zero value: the pipeline can run locally or
	// be dispatched to a remote profile. Existing pipelines without an
	// explicit Venue() method get this by default.
	VenueEither Venue = iota

	// VenueLocalOnly forbids remote dispatch (`--on PROFILE`). The
	// pipeline must run on the operator's laptop. Use for pipelines
	// that depend on laptop-only state: terraform local cache, aws
	// SSO credentials, kubeconfig, etc.
	VenueLocalOnly

	// VenueClusterOnly forbids bare invocation. The pipeline must be
	// dispatched via `--on PROFILE`. Use for pipelines that depend on
	// in-cluster state and would either fail confusingly or do nothing
	// useful from the laptop.
	VenueClusterOnly
)

// String renders Venue as the canonical kebab-case wire token used
// in JSON snapshots and error messages. The empty string is reserved
// for "unset"; the zero value renders as "either" rather than empty
// so `--describe` JSON consumers always get a deterministic value.
func (v Venue) String() string {
	switch v {
	case VenueLocalOnly:
		return "local-only"
	case VenueClusterOnly:
		return "cluster-only"
	default:
		return "either"
	}
}

// venueProvider is the optional interface a pipeline value satisfies
// to declare its venue. Mirrors the HelpProvider / ShortHelpProvider
// pattern: Plan() stays the only required method on Pipeline[T] and
// metadata is layered via type-assertion.
type venueProvider interface {
	Venue() Venue
}

// PipelineVenue returns the declared Venue for a registered
// pipeline. Pipelines that don't implement Venue() return
// VenueEither, preserving the existing "dispatch anywhere"
// behavior. A nil Registration also returns VenueEither so callers
// don't have to nil-check before consulting venue.
func PipelineVenue(reg *Registration) Venue {
	if reg == nil || reg.instance == nil {
		return VenueEither
	}
	inst := reg.instance()
	if inst == nil {
		return VenueEither
	}
	if vp, ok := inst.(venueProvider); ok {
		return vp.Venue()
	}
	return VenueEither
}

// EnforceVenue returns a non-nil error when the supplied venue +
// remote-profile pair is incompatible. Centralizes the canonical
// error messages so the CLI dispatcher and any future agent /
// dashboard surface print identical text. The `on` argument is the
// remote profile name (empty for a bare laptop invocation).
//
// Caller is expected to short-circuit dispatch on a non-nil return.
func EnforceVenue(v Venue, name, on string) error {
	switch v {
	case VenueLocalOnly:
		if on != "" {
			return &VenueMismatchError{
				Pipeline: name,
				Venue:    v,
				On:       on,
				Reason:   "remote-dispatch",
			}
		}
	case VenueClusterOnly:
		if on == "" {
			return &VenueMismatchError{
				Pipeline: name,
				Venue:    v,
				On:       "",
				Reason:   "bare-invocation",
			}
		}
	}
	return nil
}

// VenueMismatchError is the typed error EnforceVenue returns when
// the dispatch shape conflicts with the declared venue. Carries
// enough structured data that JSON consumers (agents, dashboard)
// can react without parsing the message.
type VenueMismatchError struct {
	Pipeline string
	Venue    Venue
	On       string
	// Reason is one of: "remote-dispatch" (LocalOnly + --on),
	// "bare-invocation" (ClusterOnly without --on).
	Reason string
}

func (e *VenueMismatchError) Error() string {
	switch e.Reason {
	case "remote-dispatch":
		return "pipeline \"" + e.Pipeline + "\" declares venue=local-only and cannot be dispatched to profile \"" + e.On + "\". Drop --on or change Venue to ClusterOnly/Either."
	case "bare-invocation":
		return "pipeline \"" + e.Pipeline + "\" declares venue=cluster-only and requires --on PROFILE."
	default:
		return "pipeline \"" + e.Pipeline + "\" venue mismatch (" + e.Venue.String() + ", on=\"" + e.On + "\")"
	}
}
