package wingd

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// LocalActivity counts live state, not historical runs or configured schedules.
type LocalActivity struct {
	Runs, QueuedTriggers, HeldLeases, DashboardConnections int
	IdleSince                                              time.Time
}

// MayExitIdle requires every activity count to be zero and --stay to be false.
func MayExitIdle(now time.Time, activity LocalActivity, stay bool) bool {
	return !stay && !activity.IdleSince.IsZero() &&
		activity.Runs == 0 && activity.QueuedTriggers == 0 &&
		activity.HeldLeases == 0 && activity.DashboardConnections == 0 &&
		now.Sub(activity.IdleSince) >= limits.LocalControllerIdleTimeout
}

// LocalPeerIdentity comes from the transport's kernel credentials, never request input.
type LocalPeerIdentity struct {
	GOOS string
	UID  *uint32
	SID  string
}

// LocalSubject checks the peer against the controller's OS account before minting local authority.
func LocalSubject(peer, owner LocalPeerIdentity) (string, error) {
	if peer.GOOS != owner.GOOS {
		return "", fmt.Errorf("wingd: peer platform does not match controller platform")
	}
	switch peer.GOOS {
	case "linux", "darwin":
		if peer.UID == nil || owner.UID == nil || *peer.UID != *owner.UID {
			return "", fmt.Errorf("wingd: local access requires the controller account's kernel UID")
		}
		return fmt.Sprintf("local:%d", *peer.UID), nil
	case "windows":
		if peer.SID == "" || owner.SID == "" || peer.SID != owner.SID {
			return "", fmt.Errorf("wingd: local access requires the controller account's client SID")
		}
		return "local:" + peer.SID, nil
	default:
		return "", fmt.Errorf("wingd: unsupported local platform %q", peer.GOOS)
	}
}

// LocalStartOptions carries spawn plumbing in arguments rather than environment variables.
type LocalStartOptions struct {
	Home, Executable, Version string
	Stay, Standalone          bool
	DashboardAddress          string
}

// LocalRuntime uses the same pkg/controller implementation, route policy and state owner.
type LocalRuntime interface {
	Run(context.Context, LocalStartOptions) error
	// AwaitReady waits for the next active Run's API, store and startup reconciliation.
	// A completed previous Run cannot satisfy a new readiness request.
	AwaitReady(context.Context) (LocalStatus, error)
	Status(context.Context) (LocalStatus, error)
	SetStay(context.Context, bool) error
	// EnsureDashboard starts the UI on an already-ready owner, reusing an identical address.
	// A different address is refused until the controller is stopped and restarted.
	EnsureDashboard(context.Context, string) error
	// Drain returns after the admission drain and controller shutdown complete.
	Drain(context.Context) error
}

// LocalStatus exposes version skew and activity without opening a second writing store handle.
type LocalStatus struct {
	Running, Stay, Installed bool
	PID                      int
	Version                  string
	Activity                 LocalActivity
}

// ManagedUnitKind gives schedules and the controller one upgrade path.
type ManagedUnitKind string

const (
	ManagedController ManagedUnitKind = "controller"
	ManagedSchedule   ManagedUnitKind = "schedule"
)

// ManagedUnitPolicy records stable argv so command renames cannot strand installed timers.
type ManagedUnitPolicy struct {
	Kind             ManagedUnitKind
	Arguments        []string
	StartAtLogin     bool
	RestartOnFailure bool
	SocketActivation bool
}

// ManagedUnitPolicies covers every Sparkwing-managed unit, with no socket activation.
func ManagedUnitPolicies() []ManagedUnitPolicy {
	return []ManagedUnitPolicy{
		{Kind: ManagedController, Arguments: []string{"wingd", "run", "--stay"}, StartAtLogin: true, RestartOnFailure: true},
		{Kind: ManagedSchedule, Arguments: []string{"__tick"}},
	}
}

// ManagedUnit records rendered argv for doctor to detect removed verbs as well as stale versions.
type ManagedUnit struct {
	Kind       ManagedUnitKind
	Name       string
	Executable string
	Version    string
	Arguments  []string
	Home       string
}

// ManagedUnitManager is shared by update, dashboard installation and schedule installation.
type ManagedUnitManager interface {
	InstallController(context.Context, LocalStartOptions) error
	UninstallController(context.Context) error
	Refresh(context.Context, string, string) error
	Inspect(context.Context) ([]ManagedUnit, error)
}

// LocalDoctorFinding makes incompatible stores, version skew and removed unit verbs actionable.
type LocalDoctorFinding struct {
	Code, Unit, Message, Hint string
}

// LocalLifecycle starts on demand and shares the runtime used by explicit dashboard commands.
type LocalLifecycle interface {
	EnsureStarted(context.Context, LocalStartOptions) (LocalStatus, error)
	StartDashboard(context.Context, LocalStartOptions) (LocalStatus, error)
	Stop(context.Context) error
	Status(context.Context) (LocalStatus, error)
	Doctor(context.Context, string) ([]LocalDoctorFinding, error)
}

// NewLocalLifecycle keeps service management outside controller request handling.
func NewLocalLifecycle(runtime LocalRuntime, units ManagedUnitManager) LocalLifecycle {
	return &localLifecycle{runtime: runtime, units: units, gate: make(chan struct{}, 1)}
}
