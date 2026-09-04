package executionpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/secretname"
)

const (
	assistedExecutionPolicyRequirement = "assisted-execution-policy-v1"
	NodeExecutionPolicyVersion         = 1
	AssistedBodyProtocolVersion        = 1
	fleetSupervisorRuntimeRequirement  = "fleet-supervisor-v1"
	fleetBodyRuntimeRequirement        = "fleet-body-v1"
	executorLocationCoordinator        = "coordinator"
	executorLocationLocal              = "local"
	executorLocationCloud              = "cloud"
	executorLocationUnknown            = "unknown"

	maxExecutionPolicyBytes              = 256 << 10
	maxExecutionPolicyItems              = 1024
	maxExecutionPolicyStringBytes        = 1024
	maxExecutionOutputBytes       uint64 = 256 << 30
	maxExecutionFiles                    = 100_000
	maxExecutionEvents                   = 100_000
	maxExecutionEventBytes               = 1 << 20
)

var (
	ErrExecutionPolicyInvalid    = errors.New("invalid node execution policy")
	ErrExecutionPolicyConflict   = errors.New("node execution policy conflicts with sealed policy")
	ErrExecutionUpgradeRequired  = errors.New("executor upgrade required")
	ErrExecutionProtocolMismatch = errors.New("executor body protocol is incompatible")
)

// NodeExecutionPolicy is the complete authority granted to one assisted node
// body. It contains names and content identities, never secret values or an
// open-ended route/body capability.
type NodeExecutionPolicy struct {
	Version                int                       `json:"version"`
	Pipeline               string                    `json:"pipeline"`
	NodeID                 string                    `json:"node_id"`
	AllowedLocations       []string                  `json:"allowed_locations"`
	Dependencies           []NodeDependencyAuthority `json:"dependencies"`
	SecretNames            []string                  `json:"secret_names,omitempty"`
	ArtifactInputs         []NodeArtifactInput       `json:"artifact_inputs,omitempty"`
	ArtifactOutputs        []NodeArtifactOutput      `json:"artifact_outputs,omitempty"`
	ArtifactOutputMaxBytes uint64                    `json:"artifact_output_max_bytes"`
	ResultMaxBytes         uint64                    `json:"result_max_bytes"`
	Emission               NodeEmissionAuthority     `json:"emission"`
	SupervisorRequirements []string                  `json:"supervisor_requirements"`
	Body                   NodeCompiledBodyAuthority `json:"body"`
	Actions                []NodeExecutionGrant      `json:"actions,omitempty"`
}

// NodeDependencyAuthority grants bounded access to one whole upstream output.
// Structured projection requires a separately versioned protocol and is not
// represented by this policy version.
type NodeDependencyAuthority struct {
	NodeID      string `json:"node_id"`
	WholeOutput bool   `json:"whole_output"`
	MaxBytes    uint64 `json:"max_bytes"`
}

// NodeArtifactInput binds an input to the producer and immutable manifest it
// was staged from. Into is a relative workspace destination.
type NodeArtifactInput struct {
	ProducerNodeID   string `json:"producer_node_id"`
	ManifestDigest   string `json:"manifest_digest"`
	Into             string `json:"into,omitempty"`
	MaxBytes         uint64 `json:"max_bytes"`
	MaxFiles         int    `json:"max_files"`
	MaxManifestBytes int    `json:"max_manifest_bytes"`
}

// NodeArtifactOutput limits one declared relative output glob.
type NodeArtifactOutput struct {
	Glob     string `json:"glob"`
	MaxBytes uint64 `json:"max_bytes"`
	MaxFiles int    `json:"max_files"`
}

type NodeExecutionEventKind string

const (
	NodeExecutionEventAnnotation NodeExecutionEventKind = "annotation"
	NodeExecutionEventSummary    NodeExecutionEventKind = "summary"
	NodeExecutionEventLog        NodeExecutionEventKind = "log"
)

// NodeEmissionAuthority bounds the structured events and declared step IDs a
// body may emit. Protocol start/finish messages are not grants.
type NodeEmissionAuthority struct {
	Events          []NodeExecutionEventKind `json:"events,omitempty"`
	StepIDs         []string                 `json:"step_ids,omitempty"`
	MaxEvents       int                      `json:"max_events"`
	MaxPayloadBytes int                      `json:"max_payload_bytes"`
	MaxTotalBytes   int                      `json:"max_total_bytes"`
}

// NodeCompiledBodyAuthority identifies the compiled workload independently
// from the runner supervisor. The runner build identity is observational and
// is not evidence that these source and manifest identities match.
type NodeCompiledBodyAuthority struct {
	ProtocolVersion     int                     `json:"protocol_version"`
	RuntimeRequirements []string                `json:"runtime_requirements"`
	Source              NodeBodySourceAuthority `json:"source"`
}

// NodeBodySourceAuthority identifies source authorized at ready. A compiled
// binary digest is attested separately before an execution starts.
type NodeBodySourceAuthority struct {
	Kind           string `json:"kind"`
	Identity       string `json:"identity"`
	ManifestDigest string `json:"manifest_digest"`
	PlanDigest     string `json:"plan_digest"`
}

type NodeExecutionActionKind string

const (
	NodeExecutionActionToolSlot NodeExecutionActionKind = "tool_slot"
	NodeExecutionActionAwait    NodeExecutionActionKind = "await"
	NodeExecutionActionRef      NodeExecutionActionKind = "ref"
	NodeExecutionActionSpawn    NodeExecutionActionKind = "spawn"
)

// NodeExecutionGrant is a closed union. Exactly one member matching Kind must
// be present; callers cannot mint arbitrary controller routes or request bodies.
type NodeExecutionGrant struct {
	GrantID  string                  `json:"grant_id"`
	Kind     NodeExecutionActionKind `json:"kind"`
	ToolSlot *NodeToolSlotGrant      `json:"tool_slot,omitempty"`
	Await    *NodeAwaitGrant         `json:"await,omitempty"`
	Ref      *NodeRefGrant           `json:"ref,omitempty"`
	Spawn    *NodeSpawnGrant         `json:"spawn,omitempty"`
}

type NodeToolSlotGrant struct {
	Group              string `json:"group"`
	Scope              string `json:"scope"`
	Capacity           int    `json:"capacity"`
	OnLimit            string `json:"on_limit"`
	MaxCost            int    `json:"max_cost"`
	QueueTimeoutNanos  int64  `json:"queue_timeout_nanos"`
	CancelTimeoutNanos int64  `json:"cancel_timeout_nanos"`
}

type NodeAwaitGrant struct {
	Pipeline       string `json:"pipeline"`
	NodeID         string `json:"node_id"`
	Repo           string `json:"repo,omitempty"`
	Branch         string `json:"branch,omitempty"`
	ArgsHash       string `json:"args_hash"`
	MaxArgs        int    `json:"max_args"`
	MaxArgBytes    int    `json:"max_arg_bytes"`
	TimeoutNanos   int64  `json:"timeout_nanos"`
	MaxInvocations int    `json:"max_invocations"`
}

type NodeRefGrant struct {
	Pipeline    string `json:"pipeline"`
	NodeID      string `json:"node_id"`
	MaxAgeNanos int64  `json:"max_age_nanos"`
	MaxBytes    uint64 `json:"max_bytes"`
}

type NodeSpawnGrant struct {
	SpawnID            string `json:"spawn_id"`
	TargetWorkIdentity string `json:"target_work_identity"`
	MaxChildren        int    `json:"max_children"`
}

// Sealed is the canonical result of validating one policy. Callers receive
// owned copies; a Carrier stores only its encoded immutable representation.
type Sealed struct {
	Policy                     NodeExecutionPolicy
	Canonical                  []byte
	Hash                       string
	SupervisorRequirementsHash string
	BodyRequirementsHash       string
}

// SealNew validates a policy written by the current controller.
func SealNew(policy NodeExecutionPolicy) (Sealed, error) {
	if policy.Version != NodeExecutionPolicyVersion {
		return Sealed{}, fmt.Errorf("%w: new policy version must be %d", ErrExecutionPolicyInvalid, NodeExecutionPolicyVersion)
	}
	if policy.Body.ProtocolVersion != AssistedBodyProtocolVersion {
		return Sealed{}, fmt.Errorf("%w: new body protocol must be %d", ErrExecutionPolicyInvalid, AssistedBodyProtocolVersion)
	}
	return sealExecutionPolicyV1(policy)
}

// SealPersisted validates every durable version this binary can still read.
func SealPersisted(policy NodeExecutionPolicy) (Sealed, error) {
	switch policy.Version {
	case 1:
		if policy.Body.ProtocolVersion != 1 {
			return Sealed{}, fmt.Errorf("%w: unsupported persisted body protocol %d", ErrExecutionUpgradeRequired, policy.Body.ProtocolVersion)
		}
		return sealExecutionPolicyV1(policy)
	default:
		return Sealed{}, fmt.Errorf("%w: unsupported persisted policy version %d", ErrExecutionUpgradeRequired, policy.Version)
	}
}

func sealExecutionPolicyV1(policy NodeExecutionPolicy) (Sealed, error) {
	policy = cloneExecutionPolicy(policy)
	normalized, err := normalizeExecutionPolicyV1(policy)
	if err != nil {
		return Sealed{}, err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return Sealed{}, fmt.Errorf("%w: encode: %v", ErrExecutionPolicyInvalid, err)
	}
	if len(canonical) > maxExecutionPolicyBytes {
		return Sealed{}, fmt.Errorf("%w: encoded policy exceeds %d bytes", ErrExecutionPolicyInvalid, maxExecutionPolicyBytes)
	}
	return Sealed{
		Policy: normalized, Canonical: canonical, Hash: contentHash(canonical),
		SupervisorRequirementsHash: stringListHash(normalized.SupervisorRequirements),
		BodyRequirementsHash:       stringListHash(normalized.Body.RuntimeRequirements),
	}, nil
}

// Decode validates one canonical persisted policy document.
func Decode(raw []byte) (Sealed, error) {
	if len(raw) == 0 || len(raw) > maxExecutionPolicyBytes {
		return Sealed{}, fmt.Errorf("%w: encoded policy size", ErrExecutionPolicyInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var policy NodeExecutionPolicy
	if err := dec.Decode(&policy); err != nil {
		return Sealed{}, fmt.Errorf("%w: decode: %v", ErrExecutionPolicyInvalid, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Sealed{}, fmt.Errorf("%w: trailing JSON: %v", ErrExecutionPolicyInvalid, err)
	}
	return SealPersisted(policy)
}

// Carrier holds a complete policy seal without exposing mutable policy state.
// Its unexported method lets repository-internal persistence packages operate
// on owners that embed it without adding methods to the owner's public API.
type Carrier struct {
	policyJSON                 []byte
	policyHash                 string
	policyVersion              int
	bodyProtocol               int
	supervisorRequirementsJSON []byte
	supervisorRequirementsHash string
	bodyRequirementsJSON       []byte
	bodyRequirementsHash       string
}

func (c *Carrier) carrier() *Carrier { return c }

type carrierOwner interface {
	carrier() *Carrier
}

// Binding is the persisted node identity and scheduling tuple a policy seals.
type Binding struct {
	Pipeline                 string
	NodeID                   string
	Dependencies             []string
	NeedsLabels              []string
	RequiredExecutorLocation string
}

// Persistence is the complete private SQL representation of a carrier.
type Persistence struct {
	PolicyJSON                 []byte
	PolicyHash                 string
	PolicyVersion              int
	BodyProtocol               int
	SupervisorRequirementsJSON []byte
	SupervisorRequirementsHash string
	BodyRequirementsJSON       []byte
	BodyRequirementsHash       string
}

type wireCarrier struct {
	PolicyJSON                 json.RawMessage `json:"policy"`
	PolicyHash                 string          `json:"policy_hash"`
	PolicyVersion              int             `json:"policy_version"`
	BodyProtocol               int             `json:"body_protocol"`
	SupervisorRequirementsJSON json.RawMessage `json:"supervisor_requirements"`
	SupervisorRequirementsHash string          `json:"supervisor_requirements_hash"`
	BodyRequirementsJSON       json.RawMessage `json:"body_requirements"`
	BodyRequirementsHash       string          `json:"body_requirements_hash"`
}

// SetNew validates and installs a newly authored policy on owner.
func SetNew(owner carrierOwner, policy NodeExecutionPolicy) error {
	sealed, err := SealNew(policy)
	if err != nil {
		return err
	}
	setCarrier(owner.carrier(), persistenceFromSealed(sealed))
	return nil
}

// Restore installs an exact SQL tuple, rejecting partial or corrupt seals.
func Restore(owner carrierOwner, persisted Persistence) error {
	carrier, err := carrierFromPersistence(persisted)
	if err != nil {
		return err
	}
	*owner.carrier() = carrier
	return nil
}

// PersistenceOf returns an owned copy of the complete durable tuple.
func PersistenceOf(owner carrierOwner) (Persistence, error) {
	carrier := cloneCarrier(*owner.carrier())
	if _, _, err := validateCarrier(carrier); err != nil {
		return Persistence{}, err
	}
	return Persistence{
		PolicyJSON: append([]byte(nil), carrier.policyJSON...), PolicyHash: carrier.policyHash,
		PolicyVersion: carrier.policyVersion, BodyProtocol: carrier.bodyProtocol,
		SupervisorRequirementsJSON: append([]byte(nil), carrier.supervisorRequirementsJSON...),
		SupervisorRequirementsHash: carrier.supervisorRequirementsHash,
		BodyRequirementsJSON:       append([]byte(nil), carrier.bodyRequirementsJSON...),
		BodyRequirementsHash:       carrier.bodyRequirementsHash,
	}, nil
}

// EncodeCarrier encodes an owner's private carrier for non-SQL persistence.
func EncodeCarrier(owner carrierOwner) ([]byte, error) {
	persisted, err := PersistenceOf(owner)
	if err != nil {
		return nil, err
	}
	if persistenceIsZero(persisted) {
		return nil, nil
	}
	return json.Marshal(wireCarrier{
		PolicyJSON: persisted.PolicyJSON, PolicyHash: persisted.PolicyHash,
		PolicyVersion: persisted.PolicyVersion, BodyProtocol: persisted.BodyProtocol,
		SupervisorRequirementsJSON: persisted.SupervisorRequirementsJSON,
		SupervisorRequirementsHash: persisted.SupervisorRequirementsHash,
		BodyRequirementsJSON:       persisted.BodyRequirementsJSON,
		BodyRequirementsHash:       persisted.BodyRequirementsHash,
	})
}

// DecodeCarrierStrict restores one private carrier and rejects extensions or
// trailing documents before changing owner.
func DecodeCarrierStrict(raw []byte, owner carrierOwner) error {
	if len(raw) == 0 {
		ClearCarrier(owner)
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire wireCarrier
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("%w: decode private carrier: %v", ErrExecutionPolicyInvalid, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing private carrier JSON: %v", ErrExecutionPolicyInvalid, err)
	}
	return Restore(owner, Persistence{
		PolicyJSON: wire.PolicyJSON, PolicyHash: wire.PolicyHash,
		PolicyVersion: wire.PolicyVersion, BodyProtocol: wire.BodyProtocol,
		SupervisorRequirementsJSON: wire.SupervisorRequirementsJSON,
		SupervisorRequirementsHash: wire.SupervisorRequirementsHash,
		BodyRequirementsJSON:       wire.BodyRequirementsJSON,
		BodyRequirementsHash:       wire.BodyRequirementsHash,
	})
}

// CopyCarrier gives dst an immutable copy of src's carrier.
func CopyCarrier(dst, src carrierOwner) {
	*dst.carrier() = cloneCarrier(*src.carrier())
}

// ClearCarrier restores the ordinary deny-all carrier.
func ClearCarrier(owner carrierOwner) { *owner.carrier() = Carrier{} }

// IsSealed reports whether owner carries an assisted-execution policy.
func IsSealed(owner carrierOwner) bool { return len(owner.carrier().policyJSON) != 0 }

// Equal reports whether two owners carry the same complete private tuple.
func Equal(left, right carrierOwner) bool {
	a, aerr := PersistenceOf(left)
	b, berr := PersistenceOf(right)
	return aerr == nil && berr == nil && persistenceEqual(a, b)
}

// PolicyOf returns an owned policy copy when owner is sealed.
func PolicyOf(owner carrierOwner) (NodeExecutionPolicy, bool, error) {
	sealed, present, err := validateCarrier(*owner.carrier())
	if err != nil || !present {
		return NodeExecutionPolicy{}, present, err
	}
	return cloneExecutionPolicy(sealed.Policy), true, nil
}

// ValidateForNode binds a complete seal to its stored node identity,
// dependencies, and assisted placement.
func ValidateForNode(owner carrierOwner, binding Binding) error {
	sealed, present, err := validateCarrier(*owner.carrier())
	if err != nil || !present {
		return err
	}
	if sealed.Policy.Pipeline != binding.Pipeline || sealed.Policy.NodeID != binding.NodeID {
		return fmt.Errorf("%w: policy identity does not match stored node", ErrExecutionPolicyInvalid)
	}
	if !executionPolicyDepsMatch(binding.Dependencies, sealed.Policy.Dependencies) {
		return fmt.Errorf("%w: policy dependencies do not match stored node", ErrExecutionPolicyInvalid)
	}
	return validateAssistedPlacement(binding, sealed.Policy.AllowedLocations)
}

func persistenceFromSealed(sealed Sealed) Persistence {
	supervisorJSON, _ := json.Marshal(sealed.Policy.SupervisorRequirements)
	bodyJSON, _ := json.Marshal(sealed.Policy.Body.RuntimeRequirements)
	return Persistence{
		PolicyJSON: append([]byte(nil), sealed.Canonical...), PolicyHash: sealed.Hash,
		PolicyVersion: sealed.Policy.Version, BodyProtocol: sealed.Policy.Body.ProtocolVersion,
		SupervisorRequirementsJSON: supervisorJSON, SupervisorRequirementsHash: sealed.SupervisorRequirementsHash,
		BodyRequirementsJSON: bodyJSON, BodyRequirementsHash: sealed.BodyRequirementsHash,
	}
}

func carrierFromPersistence(persisted Persistence) (Carrier, error) {
	if persistenceIsZero(persisted) {
		return Carrier{}, nil
	}
	if persisted.PolicyHash == "" || len(persisted.PolicyJSON) == 0 || persisted.PolicyVersion == 0 || persisted.BodyProtocol == 0 ||
		len(persisted.SupervisorRequirementsJSON) == 0 || persisted.SupervisorRequirementsHash == "" ||
		len(persisted.BodyRequirementsJSON) == 0 || persisted.BodyRequirementsHash == "" {
		return Carrier{}, fmt.Errorf("%w: partial private execution seal", ErrExecutionPolicyInvalid)
	}
	sealed, err := Decode(persisted.PolicyJSON)
	if err != nil {
		return Carrier{}, err
	}
	if sealed.Hash != persisted.PolicyHash || sealed.Policy.Version != persisted.PolicyVersion ||
		sealed.Policy.Body.ProtocolVersion != persisted.BodyProtocol ||
		sealed.SupervisorRequirementsHash != persisted.SupervisorRequirementsHash || sealed.BodyRequirementsHash != persisted.BodyRequirementsHash {
		return Carrier{}, fmt.Errorf("%w: persisted execution seal mismatch", ErrExecutionPolicyInvalid)
	}
	var supervisor, body []string
	if err := json.Unmarshal(persisted.SupervisorRequirementsJSON, &supervisor); err != nil || !slices.Equal(supervisor, sealed.Policy.SupervisorRequirements) {
		return Carrier{}, fmt.Errorf("%w: persisted supervisor requirements mismatch", ErrExecutionPolicyInvalid)
	}
	if err := json.Unmarshal(persisted.BodyRequirementsJSON, &body); err != nil || !slices.Equal(body, sealed.Policy.Body.RuntimeRequirements) {
		return Carrier{}, fmt.Errorf("%w: persisted body requirements mismatch", ErrExecutionPolicyInvalid)
	}
	var carrier Carrier
	setCarrier(&carrier, persisted)
	return carrier, nil
}

func validateCarrier(carrier Carrier) (Sealed, bool, error) {
	persisted := Persistence{
		PolicyJSON: carrier.policyJSON, PolicyHash: carrier.policyHash,
		PolicyVersion: carrier.policyVersion, BodyProtocol: carrier.bodyProtocol,
		SupervisorRequirementsJSON: carrier.supervisorRequirementsJSON,
		SupervisorRequirementsHash: carrier.supervisorRequirementsHash,
		BodyRequirementsJSON:       carrier.bodyRequirementsJSON,
		BodyRequirementsHash:       carrier.bodyRequirementsHash,
	}
	if persistenceIsZero(persisted) {
		return Sealed{}, false, nil
	}
	validated, err := carrierFromPersistence(persisted)
	if err != nil {
		return Sealed{}, false, err
	}
	sealed, err := Decode(validated.policyJSON)
	return sealed, err == nil, err
}

func setCarrier(carrier *Carrier, persisted Persistence) {
	*carrier = Carrier{
		policyJSON: append([]byte(nil), persisted.PolicyJSON...), policyHash: persisted.PolicyHash,
		policyVersion: persisted.PolicyVersion, bodyProtocol: persisted.BodyProtocol,
		supervisorRequirementsJSON: append([]byte(nil), persisted.SupervisorRequirementsJSON...),
		supervisorRequirementsHash: persisted.SupervisorRequirementsHash,
		bodyRequirementsJSON:       append([]byte(nil), persisted.BodyRequirementsJSON...),
		bodyRequirementsHash:       persisted.BodyRequirementsHash,
	}
}

func cloneCarrier(carrier Carrier) Carrier {
	carrier.policyJSON = append([]byte(nil), carrier.policyJSON...)
	carrier.supervisorRequirementsJSON = append([]byte(nil), carrier.supervisorRequirementsJSON...)
	carrier.bodyRequirementsJSON = append([]byte(nil), carrier.bodyRequirementsJSON...)
	return carrier
}

func persistenceIsZero(p Persistence) bool {
	return len(p.PolicyJSON) == 0 && p.PolicyHash == "" && p.PolicyVersion == 0 && p.BodyProtocol == 0 &&
		len(p.SupervisorRequirementsJSON) == 0 && p.SupervisorRequirementsHash == "" &&
		len(p.BodyRequirementsJSON) == 0 && p.BodyRequirementsHash == ""
}

func persistenceEqual(a, b Persistence) bool {
	return bytes.Equal(a.PolicyJSON, b.PolicyJSON) && a.PolicyHash == b.PolicyHash && a.PolicyVersion == b.PolicyVersion &&
		a.BodyProtocol == b.BodyProtocol && bytes.Equal(a.SupervisorRequirementsJSON, b.SupervisorRequirementsJSON) &&
		a.SupervisorRequirementsHash == b.SupervisorRequirementsHash && bytes.Equal(a.BodyRequirementsJSON, b.BodyRequirementsJSON) &&
		a.BodyRequirementsHash == b.BodyRequirementsHash
}

func normalizeExecutionPolicyV1(policy NodeExecutionPolicy) (NodeExecutionPolicy, error) {
	if policy.Version != 1 {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: policy version must be 1", ErrExecutionPolicyInvalid)
	}
	for field, value := range map[string]string{"pipeline": policy.Pipeline, "node_id": policy.NodeID} {
		if err := validateExactIdentity(field, value); err != nil {
			return NodeExecutionPolicy{}, err
		}
	}
	locations, err := normalizeExactList("allowed location", policy.AllowedLocations)
	policy.AllowedLocations = locations
	if err != nil || len(policy.AllowedLocations) == 0 || len(policy.AllowedLocations) > 2 {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: allowed locations must be a non-empty subset of local and cloud", ErrExecutionPolicyInvalid)
	}
	for _, location := range policy.AllowedLocations {
		if location != executorLocationLocal && location != executorLocationCloud {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: allowed location %q", ErrExecutionPolicyInvalid, location)
		}
	}
	if len(policy.Dependencies) > maxExecutionPolicyItems || len(policy.SecretNames) > maxExecutionPolicyItems ||
		len(policy.ArtifactInputs) > maxExecutionPolicyItems || len(policy.ArtifactOutputs) > maxExecutionPolicyItems ||
		len(policy.Actions) > maxExecutionPolicyItems {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: collection exceeds %d items", ErrExecutionPolicyInvalid, maxExecutionPolicyItems)
	}

	deps := make([]NodeDependencyAuthority, len(policy.Dependencies))
	for i, dep := range policy.Dependencies {
		if err := validateExactIdentity("dependency node_id", dep.NodeID); err != nil {
			return NodeExecutionPolicy{}, err
		}
		if !dep.WholeOutput || dep.MaxBytes == 0 || dep.MaxBytes > maxExecutionEventBytes {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: dependency %q must grant bounded whole-output access", ErrExecutionPolicyInvalid, dep.NodeID)
		}
		deps[i] = NodeDependencyAuthority{NodeID: dep.NodeID, WholeOutput: true, MaxBytes: dep.MaxBytes}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].NodeID < deps[j].NodeID })
	for i := 1; i < len(deps); i++ {
		if deps[i-1].NodeID == deps[i].NodeID {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: duplicate dependency %q", ErrExecutionPolicyInvalid, deps[i].NodeID)
		}
	}
	policy.Dependencies = deps

	policy.SecretNames = append([]string(nil), policy.SecretNames...)
	for _, name := range policy.SecretNames {
		if err := secretname.Validate(name); err != nil {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: secret name: %v", ErrExecutionPolicyInvalid, err)
		}
	}
	sort.Strings(policy.SecretNames)
	for i := 1; i < len(policy.SecretNames); i++ {
		if policy.SecretNames[i-1] == policy.SecretNames[i] {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: duplicate secret name %q", ErrExecutionPolicyInvalid, policy.SecretNames[i])
		}
	}
	policy.SupervisorRequirements, err = normalizeExactList("supervisor requirement", policy.SupervisorRequirements)
	if err != nil {
		return NodeExecutionPolicy{}, err
	}
	policy.Body.RuntimeRequirements, err = normalizeExactList("body runtime requirement", policy.Body.RuntimeRequirements)
	if err != nil {
		return NodeExecutionPolicy{}, err
	}
	if !slices.Contains(policy.SupervisorRequirements, fleetSupervisorRuntimeRequirement) {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: missing supervisor requirement %q", ErrExecutionPolicyInvalid, fleetSupervisorRuntimeRequirement)
	}
	if !slices.Contains(policy.Body.RuntimeRequirements, fleetBodyRuntimeRequirement) {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: missing body requirement %q", ErrExecutionPolicyInvalid, fleetBodyRuntimeRequirement)
	}
	if slices.Contains(policy.SupervisorRequirements, assistedExecutionPolicyRequirement) || slices.Contains(policy.Body.RuntimeRequirements, assistedExecutionPolicyRequirement) {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: store requirement %q is not a runtime requirement", ErrExecutionPolicyInvalid, assistedExecutionPolicyRequirement)
	}
	if policy.Body.ProtocolVersion != 1 {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: body protocol must be 1", ErrExecutionPolicyInvalid)
	}
	if policy.Body.Source.Kind != "git" && policy.Body.Source.Kind != "working_tree" && policy.Body.Source.Kind != "artifact" {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: unknown body source kind %q", ErrExecutionPolicyInvalid, policy.Body.Source.Kind)
	}
	switch policy.Body.Source.Kind {
	case "git":
		if !validLowerHexRevision(policy.Body.Source.Identity) {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: git source identity must be an immutable lowercase commit", ErrExecutionPolicyInvalid)
		}
	case "working_tree", "artifact":
		if err := validateDigest("body source identity", policy.Body.Source.Identity); err != nil {
			return NodeExecutionPolicy{}, err
		}
	}
	if err := validateDigest("body source manifest digest", policy.Body.Source.ManifestDigest); err != nil {
		return NodeExecutionPolicy{}, err
	}
	if err := validateDigest("body plan digest", policy.Body.Source.PlanDigest); err != nil {
		return NodeExecutionPolicy{}, err
	}

	inputs := append([]NodeArtifactInput(nil), policy.ArtifactInputs...)
	for i := range inputs {
		if err := validateExactIdentity("artifact producer", inputs[i].ProducerNodeID); err != nil {
			return NodeExecutionPolicy{}, err
		}
		if err := validateDigest("artifact manifest digest", inputs[i].ManifestDigest); err != nil {
			return NodeExecutionPolicy{}, err
		}
		if err := validateRelativeLiteralPath("artifact destination", inputs[i].Into, true); err != nil {
			return NodeExecutionPolicy{}, err
		}
		if inputs[i].MaxBytes == 0 || inputs[i].MaxBytes > maxExecutionOutputBytes || inputs[i].MaxFiles < 1 || inputs[i].MaxFiles > maxExecutionFiles ||
			inputs[i].MaxManifestBytes < 1 || inputs[i].MaxManifestBytes > maxExecutionEventBytes {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: artifact input bounds", ErrExecutionPolicyInvalid)
		}
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].ProducerNodeID != inputs[j].ProducerNodeID {
			return inputs[i].ProducerNodeID < inputs[j].ProducerNodeID
		}
		return inputs[i].Into < inputs[j].Into
	})
	policy.ArtifactInputs = inputs
	for i := 1; i < len(inputs); i++ {
		if inputs[i-1].ProducerNodeID == inputs[i].ProducerNodeID && inputs[i-1].Into == inputs[i].Into {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: duplicate artifact input", ErrExecutionPolicyInvalid)
		}
	}
	for i := range inputs {
		for j := 0; j < i; j++ {
			if strings.EqualFold(inputs[i].Into, inputs[j].Into) {
				return NodeExecutionPolicy{}, fmt.Errorf("%w: artifact destinations collide on a case-insensitive filesystem", ErrExecutionPolicyInvalid)
			}
		}
	}

	outputs := append([]NodeArtifactOutput(nil), policy.ArtifactOutputs...)
	for i := range outputs {
		if err := validateRelativeGlob(outputs[i].Glob); err != nil {
			return NodeExecutionPolicy{}, err
		}
		if outputs[i].MaxBytes == 0 || outputs[i].MaxBytes > maxExecutionOutputBytes || outputs[i].MaxFiles < 1 || outputs[i].MaxFiles > maxExecutionFiles {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: output bounds", ErrExecutionPolicyInvalid)
		}
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Glob < outputs[j].Glob })
	for i := range outputs {
		for j := 0; j < i; j++ {
			if strings.EqualFold(outputs[i].Glob, outputs[j].Glob) {
				return NodeExecutionPolicy{}, fmt.Errorf("%w: duplicate output glob", ErrExecutionPolicyInvalid)
			}
		}
	}
	policy.ArtifactOutputs = outputs
	if policy.ArtifactOutputMaxBytes > maxExecutionOutputBytes || policy.ResultMaxBytes == 0 || policy.ResultMaxBytes > maxExecutionEventBytes {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: body output bounds", ErrExecutionPolicyInvalid)
	}

	if len(policy.Emission.Events) > 3 || len(policy.Emission.StepIDs) > maxExecutionPolicyItems ||
		policy.Emission.MaxEvents < 0 || policy.Emission.MaxEvents > maxExecutionEvents ||
		policy.Emission.MaxPayloadBytes < 0 || policy.Emission.MaxPayloadBytes > maxExecutionEventBytes ||
		policy.Emission.MaxTotalBytes < 0 || policy.Emission.MaxTotalBytes > maxExecutionEventBytes*maxExecutionPolicyItems {
		return NodeExecutionPolicy{}, fmt.Errorf("%w: emission bounds", ErrExecutionPolicyInvalid)
	}
	events := append([]NodeExecutionEventKind(nil), policy.Emission.Events...)
	for _, kind := range events {
		if kind != NodeExecutionEventAnnotation && kind != NodeExecutionEventSummary && kind != NodeExecutionEventLog {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: unknown event kind %q", ErrExecutionPolicyInvalid, kind)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i] < events[j] })
	events = slices.Compact(events)
	policy.Emission.Events = events
	policy.Emission.StepIDs, err = normalizeExactList("step id", policy.Emission.StepIDs)
	if err != nil {
		return NodeExecutionPolicy{}, err
	}

	actions := append([]NodeExecutionGrant(nil), policy.Actions...)
	for i := range actions {
		if err := normalizeExecutionGrant(&actions[i]); err != nil {
			return NodeExecutionPolicy{}, err
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].GrantID < actions[j].GrantID })
	for i := 1; i < len(actions); i++ {
		if actions[i-1].GrantID == actions[i].GrantID {
			return NodeExecutionPolicy{}, fmt.Errorf("%w: duplicate grant %q", ErrExecutionPolicyInvalid, actions[i].GrantID)
		}
	}
	policy.Actions = actions
	return policy, nil
}

func normalizeExecutionGrant(grant *NodeExecutionGrant) error {
	if err := validateExactIdentity("grant_id", grant.GrantID); err != nil {
		return err
	}
	present := 0
	for _, ok := range []bool{grant.ToolSlot != nil, grant.Await != nil, grant.Ref != nil, grant.Spawn != nil} {
		if ok {
			present++
		}
	}
	if present != 1 {
		return fmt.Errorf("%w: grant %q must have exactly one action", ErrExecutionPolicyInvalid, grant.GrantID)
	}
	switch grant.Kind {
	case NodeExecutionActionToolSlot:
		if grant.ToolSlot != nil && grant.ToolSlot.MaxCost >= 1 && grant.ToolSlot.MaxCost <= grant.ToolSlot.Capacity && grant.ToolSlot.Capacity <= 1_000_000 &&
			(grant.ToolSlot.Scope == "run" || grant.ToolSlot.Scope == "box" || grant.ToolSlot.Scope == "global") &&
			(grant.ToolSlot.OnLimit == "queue" || grant.ToolSlot.OnLimit == "fail" || grant.ToolSlot.OnLimit == "skip" || grant.ToolSlot.OnLimit == "cancel_others") &&
			grant.ToolSlot.QueueTimeoutNanos >= 0 && grant.ToolSlot.CancelTimeoutNanos >= 0 &&
			validateExactIdentity("tool slot group", grant.ToolSlot.Group) == nil {
			return nil
		}
	case NodeExecutionActionAwait:
		if grant.Await != nil && grant.Await.MaxArgs >= 0 && grant.Await.MaxArgs <= maxExecutionPolicyItems && grant.Await.MaxArgBytes >= 0 &&
			grant.Await.MaxArgBytes <= maxExecutionEventBytes && grant.Await.TimeoutNanos >= 0 && grant.Await.MaxInvocations >= 1 &&
			grant.Await.MaxInvocations <= maxExecutionPolicyItems && validateExactIdentity("await pipeline", grant.Await.Pipeline) == nil &&
			validateExactIdentity("await node_id", grant.Await.NodeID) == nil && validateDigest("await args hash", grant.Await.ArgsHash) == nil &&
			validateOptionalExactIdentity("await repo", grant.Await.Repo) == nil && validateOptionalExactIdentity("await branch", grant.Await.Branch) == nil {
			return nil
		}
	case NodeExecutionActionRef:
		if grant.Ref != nil && grant.Ref.MaxAgeNanos >= 0 && grant.Ref.MaxBytes > 0 && grant.Ref.MaxBytes <= maxExecutionEventBytes &&
			validateExactIdentity("ref pipeline", grant.Ref.Pipeline) == nil && validateExactIdentity("ref node_id", grant.Ref.NodeID) == nil {
			return nil
		}
	case NodeExecutionActionSpawn:
		if grant.Spawn != nil && grant.Spawn.MaxChildren >= 1 && grant.Spawn.MaxChildren <= maxExecutionPolicyItems &&
			validateExactIdentity("spawn id", grant.Spawn.SpawnID) == nil && validateDigest("spawn target work identity", grant.Spawn.TargetWorkIdentity) == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: grant %q kind/payload mismatch", ErrExecutionPolicyInvalid, grant.GrantID)
}

func normalizeExactList(field string, values []string) ([]string, error) {
	if len(values) > maxExecutionPolicyItems {
		return nil, fmt.Errorf("%w: %s exceeds %d items", ErrExecutionPolicyInvalid, field, maxExecutionPolicyItems)
	}
	out := append([]string(nil), values...)
	for _, value := range out {
		if err := validateExactIdentity(field, value); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i-1] == out[i] {
			return nil, fmt.Errorf("%w: duplicate %s %q", ErrExecutionPolicyInvalid, field, out[i])
		}
	}
	return out, nil
}

func validateExactIdentity(field, value string) error {
	if value == "" || len(value) > maxExecutionPolicyStringBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s is not an exact bounded identity", ErrExecutionPolicyInvalid, field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains a control character", ErrExecutionPolicyInvalid, field)
		}
	}
	return nil
}

func validateOptionalExactIdentity(field, value string) error {
	if value == "" {
		return nil
	}
	return validateExactIdentity(field, value)
}

func cloneExecutionPolicy(policy NodeExecutionPolicy) NodeExecutionPolicy {
	policy.Dependencies = append([]NodeDependencyAuthority(nil), policy.Dependencies...)
	policy.SecretNames = append([]string(nil), policy.SecretNames...)
	policy.ArtifactInputs = append([]NodeArtifactInput(nil), policy.ArtifactInputs...)
	policy.ArtifactOutputs = append([]NodeArtifactOutput(nil), policy.ArtifactOutputs...)
	policy.Emission.Events = append([]NodeExecutionEventKind(nil), policy.Emission.Events...)
	policy.Emission.StepIDs = append([]string(nil), policy.Emission.StepIDs...)
	policy.SupervisorRequirements = append([]string(nil), policy.SupervisorRequirements...)
	policy.Body.RuntimeRequirements = append([]string(nil), policy.Body.RuntimeRequirements...)
	policy.Actions = append([]NodeExecutionGrant(nil), policy.Actions...)
	for i := range policy.Actions {
		if policy.Actions[i].ToolSlot != nil {
			value := *policy.Actions[i].ToolSlot
			policy.Actions[i].ToolSlot = &value
		}
		if policy.Actions[i].Await != nil {
			value := *policy.Actions[i].Await
			policy.Actions[i].Await = &value
		}
		if policy.Actions[i].Ref != nil {
			value := *policy.Actions[i].Ref
			policy.Actions[i].Ref = &value
		}
		if policy.Actions[i].Spawn != nil {
			value := *policy.Actions[i].Spawn
			policy.Actions[i].Spawn = &value
		}
	}
	return policy
}

func validateDigest(field, value string) error {
	if err := validateExactIdentity(field, value); err != nil {
		return err
	}
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%w: %s must be a sha256 digest", ErrExecutionPolicyInvalid, field)
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || value != strings.ToLower(value) {
		return fmt.Errorf("%w: %s must be a sha256 digest", ErrExecutionPolicyInvalid, field)
	}
	return nil
}

func validLowerHexRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateRelativeLiteralPath(field, value string, emptyOK bool) error {
	if value == "" && emptyOK {
		return nil
	}
	if err := validateExactIdentity(field, value); err != nil {
		return err
	}
	if strings.ContainsAny(value, `\\<>:"|?*`) || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%w: %s must be a canonical relative path", ErrExecutionPolicyInvalid, field)
	}
	for _, component := range strings.Split(value, "/") {
		if !portableLiteralPathComponent(component) {
			return fmt.Errorf("%w: %s contains a non-portable path component %q", ErrExecutionPolicyInvalid, field, component)
		}
	}
	return nil
}

func portableLiteralPathComponent(component string) bool {
	if component == "" || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return false
	}
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	return !reservedWindowsDevice(base)
}

func validateRelativeGlob(value string) error {
	if err := validateExactIdentity("output glob", value); err != nil {
		return err
	}
	if strings.ContainsAny(value, `\\<>:"|`) || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%w: output glob must be a canonical relative slash path", ErrExecutionPolicyInvalid)
	}
	if _, err := path.Match(value, "probe"); err != nil {
		return fmt.Errorf("%w: invalid output glob: %v", ErrExecutionPolicyInvalid, err)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return fmt.Errorf("%w: output glob contains a non-portable path component %q", ErrExecutionPolicyInvalid, component)
		}
		if !strings.ContainsAny(component, "*?[") {
			if !portableLiteralPathComponent(component) {
				return fmt.Errorf("%w: output glob contains a non-portable path component %q", ErrExecutionPolicyInvalid, component)
			}
			continue
		}
		if device, matches := globMatchesWindowsDevice(component); matches {
			return fmt.Errorf("%w: output glob can match reserved Windows device %q", ErrExecutionPolicyInvalid, device)
		}
	}
	return nil
}

func globMatchesWindowsDevice(component string) (string, bool) {
	pattern := strings.ToUpper(component)
	for _, device := range windowsDeviceAliases() {
		device = strings.ToUpper(device)
		if matches, _ := path.Match(pattern, device); matches {
			return device, true
		}
		for end := 1; end <= len(pattern); end++ {
			if matches, _ := path.Match(pattern[:end], device+"."); matches {
				return device, true
			}
		}
	}
	return "", false
}

func reservedWindowsDevice(component string) bool {
	for _, device := range windowsDeviceAliases() {
		if strings.EqualFold(component, device) {
			return true
		}
	}
	return false
}

func windowsDeviceAliases() []string {
	return []string{
		"CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³",
	}
}

func contentHash(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stringListHash(values []string) string {
	raw, _ := json.Marshal(values)
	return contentHash(raw)
}

func executionPolicyDepsMatch(stored []string, policy []NodeDependencyAuthority) bool {
	if len(stored) != len(policy) {
		return false
	}
	ids := append([]string(nil), stored...)
	sort.Strings(ids)
	for i := range ids {
		if ids[i] != policy[i].NodeID || i > 0 && ids[i] == ids[i-1] {
			return false
		}
	}
	return true
}

func validateAssistedPlacement(binding Binding, declared []string) error {
	allowed := []string{executorLocationCloud, executorLocationLocal}
	if binding.RequiredExecutorLocation != "" {
		var err error
		allowed, err = narrowAssistedLocations(allowed, []string{binding.RequiredExecutorLocation})
		if err != nil {
			return err
		}
	}
	for _, term := range binding.NeedsLabels {
		termPlacements, found, err := assistedPlacementFromTerm(term)
		if err != nil {
			return err
		}
		if found {
			allowed, err = narrowAssistedLocations(allowed, termPlacements)
			if err != nil {
				return err
			}
		}
	}
	if !slices.Equal(allowed, declared) {
		return fmt.Errorf("%w: policy allowed locations %v do not match stored requirements %v", ErrExecutionPolicyInvalid, declared, allowed)
	}
	return nil
}

func assistedPlacementFromTerm(term string) ([]string, bool, error) {
	var found []string
	sawNonPlacement := false
	for _, alternative := range strings.Split(term, ",") {
		alternative = strings.TrimSpace(alternative)
		if alternative == "" {
			return nil, false, fmt.Errorf("%w: placement term %q has an empty alternative", ErrExecutionPolicyInvalid, term)
		}
		placement := ""
		switch {
		case alternative == "local":
			placement = executorLocationCoordinator
		case strings.HasPrefix(alternative, "location="):
			placement = strings.TrimPrefix(alternative, "location=")
		default:
			sawNonPlacement = true
			continue
		}
		if sawNonPlacement {
			return nil, false, fmt.Errorf("%w: placement term %q mixes placement and non-placement alternatives", ErrExecutionPolicyInvalid, term)
		}
		if !slices.Contains(found, placement) {
			found = append(found, placement)
		}
	}
	if len(found) > 0 && sawNonPlacement {
		return nil, false, fmt.Errorf("%w: placement term %q mixes placement and non-placement alternatives", ErrExecutionPolicyInvalid, term)
	}
	if len(found) > 1 {
		return nil, false, fmt.Errorf("%w: placement term %q is ambiguous", ErrExecutionPolicyInvalid, term)
	}
	return found, len(found) != 0, nil
}

func narrowAssistedLocations(current, required []string) ([]string, error) {
	for _, location := range required {
		if location == executorLocationCoordinator || location == executorLocationUnknown {
			return nil, fmt.Errorf("%w: assisted execution cannot target %s", ErrExecutionPolicyInvalid, location)
		}
		if location != executorLocationLocal && location != executorLocationCloud {
			return nil, fmt.Errorf("%w: unknown stored placement %q", ErrExecutionPolicyInvalid, location)
		}
	}
	var out []string
	for _, location := range current {
		if slices.Contains(required, location) {
			out = append(out, location)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: stored placement requirements conflict", ErrExecutionPolicyInvalid)
	}
	return out, nil
}
