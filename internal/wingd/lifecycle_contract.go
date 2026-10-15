package wingd

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// safety: KeepAliveReason separates the four activity holds from the explicit --stay policy.
type KeepAliveReason string

const (
	KeepAliveRun       KeepAliveReason = "run"
	KeepAliveTrigger   KeepAliveReason = "queued_trigger"
	KeepAliveLease     KeepAliveReason = "held_lease"
	KeepAliveDashboard KeepAliveReason = "dashboard_connection"
)

// safety: KeepAliveReasons is exhaustive; health probes alone must not prevent idle exit.
func KeepAliveReasons() []KeepAliveReason {
	return []KeepAliveReason{KeepAliveRun, KeepAliveTrigger, KeepAliveLease, KeepAliveDashboard}
}

// safety: LocalActivity counts live state, not historical runs or configured schedules.
type LocalActivity struct {
	Runs, QueuedTriggers, HeldLeases, DashboardConnections int
	IdleSince                                              time.Time
}

// safety: MayExitIdle requires every activity count to be zero and --stay to be false.
// The idle interval is limits.LocalControllerIdleTimeout; a local run has no execution deadline.
func MayExitIdle(now time.Time, activity LocalActivity, stay bool) bool {
	return !stay && !activity.IdleSince.IsZero() &&
		activity.Runs == 0 && activity.QueuedTriggers == 0 &&
		activity.HeldLeases == 0 && activity.DashboardConnections == 0 &&
		now.Sub(activity.IdleSince) >= limits.LocalControllerIdleTimeout
}

// safety: LocalPlatformPolicy keeps Windows SID authentication and daemon schedules explicit.
type LocalPlatformPolicy struct {
	GOOS              string
	ServiceManager    string
	NativeTimers      bool
	DaemonSchedules   bool
	PeerIdentity      string
	PrivateFilePolicy string
}

// safety: LocalPlatformPolicies is the complete supported-platform table; unknown platforms fail closed.
func LocalPlatformPolicies() []LocalPlatformPolicy {
	return []LocalPlatformPolicy{
		{"linux", "systemd_user", true, false, "kernel_uid", "fssecure_0600"},
		{"darwin", "launchd_agent", true, false, "kernel_uid", "fssecure_0600"},
		{"windows", "", false, true, "client_sid_or_refuse", "fssecure_owner_sid_dacl"},
	}
}

// safety: LocalPeerIdentity comes from the transport's kernel credentials, never request input.
// Same-account peers are local administrators; missing Windows SID evidence refuses API access.
type LocalPeerIdentity struct {
	GOOS string
	UID  *uint32
	SID  string
}

// safety: LocalSubject checks the peer against the controller's OS account before minting local authority.
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

// safety: LocalStartOptions carries spawn plumbing in arguments rather than environment variables.
// Standalone is the in-process CI mode and must never open an already-owned store concurrently.
type LocalStartOptions struct {
	Home, Executable, Version string
	Stay, Standalone          bool
	DashboardAddress          string
}

// safety: LocalRuntime uses the same pkg/controller implementation, route policy and state owner.
// It owns state.db, admission, queue, trigger consumer, reaper and UI; the CLI still hosts local runs.
// Startup fails orphaned started local runs rather than requeueing them. Private files use fssecure.
// Each runtime is bound to one home at construction; Run must reject another home before opening it.
// Run and AwaitReady must finish cleanup and return when their contexts are cancelled.
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

// safety: LocalStatus exposes version skew and activity without opening a second writing store handle.
type LocalStatus struct {
	Running, Stay, Installed bool
	PID                      int
	Version                  string
	Activity                 LocalActivity
}

// safety: ManagedUnitKind gives schedules and the controller one upgrade path.
type ManagedUnitKind string

const (
	ManagedController ManagedUnitKind = "controller"
	ManagedSchedule   ManagedUnitKind = "schedule"
)

// safety: ManagedUnitPolicy records stable argv so command renames cannot strand installed timers.
type ManagedUnitPolicy struct {
	Kind             ManagedUnitKind
	Arguments        []string
	StartAtLogin     bool
	RestartOnFailure bool
	SocketActivation bool
}

// safety: ManagedUnitPolicies covers every Sparkwing-managed unit, with no socket activation.
func ManagedUnitPolicies() []ManagedUnitPolicy {
	return []ManagedUnitPolicy{
		{Kind: ManagedController, Arguments: []string{"dashboard", "start", "--stay"}, StartAtLogin: true, RestartOnFailure: true},
		{Kind: ManagedSchedule, Arguments: []string{"__tick"}},
	}
}

// safety: ManagedUnit records rendered argv for doctor to detect removed verbs as well as stale versions.
type ManagedUnit struct {
	Kind       ManagedUnitKind
	Name       string
	Executable string
	Version    string
	Arguments  []string
}

// safety: ManagedUnitManager is shared by update, dashboard installation and schedule installation.
// Refresh re-renders all managed units and restarts an installed controller after a successful update.
// InstallController and UninstallController refuse Windows; Windows supports --stay and daemon schedules.
type ManagedUnitManager interface {
	InstallController(context.Context, LocalStartOptions) error
	UninstallController(context.Context) error
	Refresh(context.Context, string, string) error
	Inspect(context.Context) ([]ManagedUnit, error)
}

// safety: LocalDoctorFinding makes incompatible stores, version skew and removed unit verbs actionable.
type LocalDoctorFinding struct {
	Code, Unit, Message, Hint string
}

// safety: LocalLifecycle starts on demand and shares the runtime used by explicit dashboard commands.
// EnsureStarted is idempotent under concurrent callers; Stop must use the existing admission drain.
// OS schedule ticks start it on demand; installing schedules does not create a fifth activity hold.
type LocalLifecycle interface {
	EnsureStarted(context.Context, LocalStartOptions) (LocalStatus, error)
	StartDashboard(context.Context, LocalStartOptions) (LocalStatus, error)
	Stop(context.Context) error
	Status(context.Context) (LocalStatus, error)
	Doctor(context.Context, string) ([]LocalDoctorFinding, error)
}

// safety: NewLocalLifecycle keeps service management outside controller request handling.
func NewLocalLifecycle(runtime LocalRuntime, units ManagedUnitManager) LocalLifecycle {
	return &localLifecycle{runtime: runtime, units: units, gate: make(chan struct{}, 1)}
}
