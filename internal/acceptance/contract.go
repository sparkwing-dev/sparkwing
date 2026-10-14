// Package acceptance defines the rewrite's observable acceptance contract without
// depending on the production contracts being developed alongside it.
package acceptance

import (
	"context"
	"net/http"
	"time"
)

// Area identifies the contract that must land before a scenario can execute.
type Area string

const (
	C1 Area = "C1"
	C2 Area = "C2"
	C3 Area = "C3"
	C4 Area = "C4"
)

// Placement preserves the six execution paths in architecture section 3.2.
type Placement string

const (
	Local          Placement = "local"
	LocalTriggered Placement = "local_triggered"
	RunnerHosted   Placement = "runner_hosted"
	Launcher       Placement = "launcher"
	Here           Placement = "here"
	GitHubActions  Placement = "github_actions"
)

// Lane separates hermetic proofs from native process and external service proofs.
type Lane string

const (
	Hermetic Lane = "hermetic"
	Native   Lane = "native"
	Cluster  Lane = "cluster"
	Hosted   Lane = "hosted"
)

// Target fixes every runtime dimension so a passing Linux mock cannot count as Windows or Cloud evidence.
type Target struct {
	ID         string
	Placement  Placement
	SDK        string
	OS         string
	State      string
	Data       string
	Lane       Lane
	Standalone bool
}

// Fixture describes the independent input and oracle an adapter must materialize.
type Fixture struct {
	ID     string
	Setup  string
	Oracle string
}

// Scenario keeps the stimulus and expected observations independent of product implementation tables.
type Scenario struct {
	ID         string
	Group      string
	Sections   []string
	Requires   []Area
	Placements []Placement
	OS         []string
	SDKs       []string
	Modes      []string
	Fixtures   []string
	Steps      []string
	Assertions []string
}

// Selection names a scenario and target from the catalogs rather than inventing a smaller test matrix.
type Selection struct {
	ScenarioID string
	TargetID   string
}

// Options confines process state and external resources to a caller-owned sandbox.
type Options struct {
	Root     string
	Binaries map[string]string
	Target   Target
	Fixtures []string
	External ExternalResources
}

// ExternalResources names preprovisioned test resources. Empty fields never select a user's defaults.
type ExternalResources struct {
	KubeContext       string
	Namespace         string
	CleanupAllowlist  []string
	GitHubRepository  string
	StripeKeyFile     string
	WebhookKeyFile    string
	PostgresURLFile   string
	ObjectStoreConfig string
}

// Command supplies argv directly because fixture values must not become shell syntax.
type Command struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string
}

// Result preserves exit status and redacted output as separate assertions.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Principal selects an issued fixture identity without exposing its bearer in a command or environment.
type Principal string

// Checkpoint synchronizes a fault with a committed state or process signal, never a guessed sleep.
type Checkpoint struct {
	Name       string
	RunID      string
	JobID      string
	Generation int64
	Attempt    int64
}

// Fault describes one controllable failure at a checkpoint. Kind names are specified in the contract doc.
type Fault struct {
	Kind       string
	Resource   string
	Checkpoint Checkpoint
}

// Artifact points to redacted evidence within the sandbox; digests make later QA receipts attributable.
type Artifact struct {
	Kind   string
	Path   string
	SHA256 string
}

// Evidence distinguishes an executed proof from a skipped skeleton and records cleanup failures.
type Evidence struct {
	Selection     Selection
	Revision      string
	BinaryDigests map[string]string
	RunIDs        []string
	Artifacts     []Artifact
	Assertions    []string
	Skipped       string
	CleanupErrors []string
}

// Harness drives real entry points and observes committed effects without owning product decisions.
// Implementations must register cleanup before starting each resource.
type Harness interface {
	CLI(context.Context, Command) (Result, error)
	HTTP(context.Context, Principal, *http.Request) (*http.Response, error)
	Reach(context.Context, Checkpoint) error
	Inject(context.Context, Fault) error
	AdvanceStoreClock(context.Context, time.Duration) error
	Collect(context.Context, Selection) (Evidence, error)
	Close(context.Context) error
}

// New provisions only the selected target and fixtures inside Options.Root.
// Unavailable required resources are errors in a selected QA lane, never successful skips.
func New(context.Context, Options) (Harness, error) {
	panic("unimplemented: t1-acceptance")
}

// Run executes the selected scenario's stimuli and checks every assertion against independent evidence.
// Removing a skeleton skip must be accompanied by its driver and assertions.
func Run(context.Context, Harness, Selection) (Evidence, error) {
	panic("unimplemented: t1-acceptance")
}
