package wingd

import (
	"context"
	"time"
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
	panic("unimplemented: c4-surface-config")
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
	panic("unimplemented: c4-surface-config")
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
type LocalRuntime interface {
	Run(context.Context, LocalStartOptions) error
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
	panic("unimplemented: c4-surface-config")
}
