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

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

const (
	assistedExecutionPolicyRequirement = "assisted-execution-policy-v1"
	nodeExecutionPolicyVersion         = 1
	assistedBodyProtocolVersion        = 1
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
	errExecutionPolicyInvalid   = errors.New("invalid node execution policy")
	errExecutionUpgradeRequired = errors.New("executor upgrade required")
)

// nodeExecutionPolicy is the complete authority granted to one assisted node
// body. It contains names and content identities, never secret values or an
// open-ended route/body capability.
type nodeExecutionPolicy struct {
	Version                int                       `json:"version"`
	Pipeline               string                    `json:"pipeline"`
	NodeID                 string                    `json:"node_id"`
	AllowedLocations       []string                  `json:"allowed_locations"`
	Dependencies           []nodeDependencyAuthority `json:"dependencies"`
	SecretNames            []string                  `json:"secret_names,omitempty"`
	ArtifactInputs         []nodeArtifactInput       `json:"artifact_inputs,omitempty"`
	ArtifactOutputs        []nodeArtifactOutput      `json:"artifact_outputs,omitempty"`
	ArtifactOutputMaxBytes uint64                    `json:"artifact_output_max_bytes"`
	ResultMaxBytes         uint64                    `json:"result_max_bytes"`
	Emission               nodeEmissionAuthority     `json:"emission"`
	SupervisorRequirements []string                  `json:"supervisor_requirements"`
	Body                   nodeCompiledBodyAuthority `json:"body"`
	Actions                []nodeExecutionGrant      `json:"actions,omitempty"`
}

// nodeDependencyAuthority grants bounded access to one whole upstream output.
// Structured projection requires a separately versioned protocol and is not
// represented by this policy version.
type nodeDependencyAuthority struct {
	NodeID      string `json:"node_id"`
	WholeOutput bool   `json:"whole_output"`
	MaxBytes    uint64 `json:"max_bytes"`
}

// nodeArtifactInput binds an input to the producer and immutable manifest it
// was staged from. Into is a relative workspace destination.
type nodeArtifactInput struct {
	ProducerNodeID   string `json:"producer_node_id"`
	ManifestDigest   string `json:"manifest_digest"`
	Into             string `json:"into,omitempty"`
	MaxBytes         uint64 `json:"max_bytes"`
	MaxFiles         int    `json:"max_files"`
	MaxManifestBytes int    `json:"max_manifest_bytes"`
}

// nodeArtifactOutput limits one declared relative output glob.
type nodeArtifactOutput struct {
	Glob     string `json:"glob"`
	MaxBytes uint64 `json:"max_bytes"`
	MaxFiles int    `json:"max_files"`
}

type nodeExecutionEventKind string

const (
	nodeExecutionEventAnnotation nodeExecutionEventKind = "annotation"
	nodeExecutionEventSummary    nodeExecutionEventKind = "summary"
	nodeExecutionEventLog        nodeExecutionEventKind = "log"
)

// nodeEmissionAuthority bounds the structured events and declared step IDs a
// body may emit. Protocol start/finish messages are not grants.
type nodeEmissionAuthority struct {
	Events          []nodeExecutionEventKind `json:"events,omitempty"`
	StepIDs         []string                 `json:"step_ids,omitempty"`
	MaxEvents       int                      `json:"max_events"`
	MaxPayloadBytes int                      `json:"max_payload_bytes"`
	MaxTotalBytes   int                      `json:"max_total_bytes"`
}

// nodeCompiledBodyAuthority identifies the compiled workload independently
// from the runner supervisor. The runner build identity is observational and
// is not evidence that these source and manifest identities match.
type nodeCompiledBodyAuthority struct {
	ProtocolVersion     int                     `json:"protocol_version"`
	RuntimeRequirements []string                `json:"runtime_requirements"`
	Source              nodeBodySourceAuthority `json:"source"`
}

// nodeBodySourceAuthority identifies source authorized at ready. A compiled
// binary digest is attested separately before an execution starts.
type nodeBodySourceAuthority struct {
	Kind           string `json:"kind"`
	Identity       string `json:"identity"`
	ManifestDigest string `json:"manifest_digest"`
	PlanDigest     string `json:"plan_digest"`
}

type nodeExecutionActionKind string

const (
	nodeExecutionActionToolSlot nodeExecutionActionKind = "tool_slot"
	nodeExecutionActionAwait    nodeExecutionActionKind = "await"
	nodeExecutionActionRef      nodeExecutionActionKind = "ref"
)

// nodeExecutionGrant is a closed union. Exactly one member matching Kind must
// be present; callers cannot mint arbitrary controller routes or request bodies.
type nodeExecutionGrant struct {
	GrantID  string                  `json:"grant_id"`
	Kind     nodeExecutionActionKind `json:"kind"`
	ToolSlot *nodeToolSlotGrant      `json:"tool_slot,omitempty"`
	Await    *nodeAwaitGrant         `json:"await,omitempty"`
	Ref      *nodeRefGrant           `json:"ref,omitempty"`
}

type nodeToolSlotGrant struct {
	Group             string `json:"group"`
	Scope             string `json:"scope"`
	Capacity          int    `json:"capacity"`
	OnLimit           string `json:"on_limit"`
	MaxCost           int    `json:"max_cost"`
	QueueTimeoutNanos int64  `json:"queue_timeout_nanos"`
}

type nodeAwaitGrant struct {
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

type nodeRefGrant struct {
	Pipeline    string `json:"pipeline"`
	NodeID      string `json:"node_id"`
	MaxAgeNanos int64  `json:"max_age_nanos"`
	MaxBytes    uint64 `json:"max_bytes"`
}

// sealed is the canonical result of validating one policy. Callers receive
// owned copies; a Carrier stores only its encoded immutable representation.
type sealed struct {
	Policy                     nodeExecutionPolicy
	Canonical                  []byte
	Hash                       string
	SupervisorRequirementsHash string
	BodyRequirementsHash       string
}

// sealNew validates a policy written by the current controller.
func sealNew(policy nodeExecutionPolicy) (sealed, error) {
	if policy.Version != nodeExecutionPolicyVersion {
		return sealed{}, fmt.Errorf("%w: new policy version must be %d", errExecutionPolicyInvalid, nodeExecutionPolicyVersion)
	}
	if policy.Body.ProtocolVersion != assistedBodyProtocolVersion {
		return sealed{}, fmt.Errorf("%w: new body protocol must be %d", errExecutionPolicyInvalid, assistedBodyProtocolVersion)
	}
	return sealExecutionPolicyV1(policy)
}

// sealPersisted validates every durable version this binary can still read.
func sealPersisted(policy nodeExecutionPolicy) (sealed, error) {
	switch policy.Version {
	case 1:
		if policy.Body.ProtocolVersion != 1 {
			return sealed{}, fmt.Errorf("%w: unsupported persisted body protocol %d", errExecutionUpgradeRequired, policy.Body.ProtocolVersion)
		}
		return sealExecutionPolicyV1(policy)
	default:
		return sealed{}, fmt.Errorf("%w: unsupported persisted policy version %d", errExecutionUpgradeRequired, policy.Version)
	}
}

func sealExecutionPolicyV1(policy nodeExecutionPolicy) (sealed, error) {
	policy = cloneExecutionPolicy(policy)
	normalized, err := normalizeExecutionPolicyV1(policy)
	if err != nil {
		return sealed{}, err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return sealed{}, fmt.Errorf("%w: encode: %v", errExecutionPolicyInvalid, err)
	}
	if len(canonical) > maxExecutionPolicyBytes {
		return sealed{}, fmt.Errorf("%w: encoded policy exceeds %d bytes", errExecutionPolicyInvalid, maxExecutionPolicyBytes)
	}
	return sealed{
		Policy: normalized, Canonical: canonical, Hash: contentHash(canonical),
		SupervisorRequirementsHash: stringListHash(normalized.SupervisorRequirements),
		BodyRequirementsHash:       stringListHash(normalized.Body.RuntimeRequirements),
	}, nil
}

// decode validates one canonical persisted policy document.
func decode(raw []byte) (sealed, error) {
	if len(raw) == 0 || len(raw) > maxExecutionPolicyBytes {
		return sealed{}, fmt.Errorf("%w: encoded policy size", errExecutionPolicyInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var policy nodeExecutionPolicy
	if err := dec.Decode(&policy); err != nil {
		return sealed{}, fmt.Errorf("%w: decode: %v", errExecutionPolicyInvalid, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return sealed{}, fmt.Errorf("%w: trailing JSON: %v", errExecutionPolicyInvalid, err)
	}
	return sealPersisted(policy)
}

// carrier holds a complete policy seal without exposing mutable policy state.
// Its unexported method lets repository-internal persistence packages operate
// on owners that embed it without adding methods to the owner's public API.
type carrier struct {
	policyJSON                 []byte
	policyHash                 string
	policyVersion              int
	bodyProtocol               int
	supervisorRequirementsJSON []byte
	supervisorRequirementsHash string
	bodyRequirementsJSON       []byte
	bodyRequirementsHash       string
}

func (c *carrier) carrier() *carrier { return c }

type carrierOwner interface {
	carrier() *carrier
}

// binding is the persisted node identity and scheduling tuple a policy seals.
type binding struct {
	Pipeline                 string
	NodeID                   string
	Dependencies             []string
	NeedsLabels              []string
	RequiredExecutorLocation string
}

// persistence is the complete private SQL representation of a carrier.
type persistence struct {
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

// setNew validates and installs a newly authored policy on owner.
func setNew(owner carrierOwner, policy nodeExecutionPolicy) error {
	sealed, err := sealNew(policy)
	if err != nil {
		return err
	}
	setCarrier(owner.carrier(), persistenceFromSealed(sealed))
	return nil
}

// restore installs an exact SQL tuple, rejecting partial or corrupt seals.
func restore(owner carrierOwner, persisted persistence) error {
	carrier, err := carrierFromPersistence(persisted)
	if err != nil {
		return err
	}
	*owner.carrier() = carrier
	return nil
}

// persistenceOf returns an owned copy of the complete durable tuple.
func persistenceOf(owner carrierOwner) (persistence, error) {
	carrier := cloneCarrier(*owner.carrier())
	if _, _, err := validateCarrier(carrier); err != nil {
		return persistence{}, err
	}
	return persistence{
		PolicyJSON: append([]byte(nil), carrier.policyJSON...), PolicyHash: carrier.policyHash,
		PolicyVersion: carrier.policyVersion, BodyProtocol: carrier.bodyProtocol,
		SupervisorRequirementsJSON: append([]byte(nil), carrier.supervisorRequirementsJSON...),
		SupervisorRequirementsHash: carrier.supervisorRequirementsHash,
		BodyRequirementsJSON:       append([]byte(nil), carrier.bodyRequirementsJSON...),
		BodyRequirementsHash:       carrier.bodyRequirementsHash,
	}, nil
}

// encodeCarrier encodes an owner's private carrier for non-SQL persistence.
func encodeCarrier(owner carrierOwner) ([]byte, error) {
	persisted, err := persistenceOf(owner)
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

// decodeCarrierStrict restores one private carrier and rejects extensions or
// trailing documents before changing owner.
func decodeCarrierStrict(raw []byte, owner carrierOwner) error {
	if len(raw) == 0 {
		clearCarrier(owner)
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire wireCarrier
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("%w: decode private carrier: %v", errExecutionPolicyInvalid, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing private carrier JSON: %v", errExecutionPolicyInvalid, err)
	}
	return restore(owner, persistence{
		PolicyJSON: wire.PolicyJSON, PolicyHash: wire.PolicyHash,
		PolicyVersion: wire.PolicyVersion, BodyProtocol: wire.BodyProtocol,
		SupervisorRequirementsJSON: wire.SupervisorRequirementsJSON,
		SupervisorRequirementsHash: wire.SupervisorRequirementsHash,
		BodyRequirementsJSON:       wire.BodyRequirementsJSON,
		BodyRequirementsHash:       wire.BodyRequirementsHash,
	})
}

// copyCarrier gives dst an immutable copy of src's carrier.
func copyCarrier(dst, src carrierOwner) {
	*dst.carrier() = cloneCarrier(*src.carrier())
}

// clearCarrier restores the ordinary deny-all carrier.
func clearCarrier(owner carrierOwner) { *owner.carrier() = carrier{} }

// isSealed reports whether owner carries an assisted-execution policy.
func isSealed(owner carrierOwner) bool { return len(owner.carrier().policyJSON) != 0 }

func persistenceFromSealed(sealed sealed) persistence {
	supervisorJSON, _ := json.Marshal(sealed.Policy.SupervisorRequirements)
	bodyJSON, _ := json.Marshal(sealed.Policy.Body.RuntimeRequirements)
	return persistence{
		PolicyJSON: append([]byte(nil), sealed.Canonical...), PolicyHash: sealed.Hash,
		PolicyVersion: sealed.Policy.Version, BodyProtocol: sealed.Policy.Body.ProtocolVersion,
		SupervisorRequirementsJSON: supervisorJSON, SupervisorRequirementsHash: sealed.SupervisorRequirementsHash,
		BodyRequirementsJSON: bodyJSON, BodyRequirementsHash: sealed.BodyRequirementsHash,
	}
}

func carrierFromPersistence(persisted persistence) (carrier, error) {
	if persistenceIsZero(persisted) {
		return carrier{}, nil
	}
	if persisted.PolicyHash == "" || len(persisted.PolicyJSON) == 0 || persisted.PolicyVersion == 0 || persisted.BodyProtocol == 0 ||
		len(persisted.SupervisorRequirementsJSON) == 0 || persisted.SupervisorRequirementsHash == "" ||
		len(persisted.BodyRequirementsJSON) == 0 || persisted.BodyRequirementsHash == "" {
		return carrier{}, fmt.Errorf("%w: partial private execution seal", errExecutionPolicyInvalid)
	}
	sealed, err := decode(persisted.PolicyJSON)
	if err != nil {
		return carrier{}, err
	}
	if sealed.Hash != persisted.PolicyHash || sealed.Policy.Version != persisted.PolicyVersion ||
		sealed.Policy.Body.ProtocolVersion != persisted.BodyProtocol ||
		sealed.SupervisorRequirementsHash != persisted.SupervisorRequirementsHash || sealed.BodyRequirementsHash != persisted.BodyRequirementsHash {
		return carrier{}, fmt.Errorf("%w: persisted execution seal mismatch", errExecutionPolicyInvalid)
	}
	var supervisor, body []string
	if err := json.Unmarshal(persisted.SupervisorRequirementsJSON, &supervisor); err != nil || !slices.Equal(supervisor, sealed.Policy.SupervisorRequirements) {
		return carrier{}, fmt.Errorf("%w: persisted supervisor requirements mismatch", errExecutionPolicyInvalid)
	}
	if err := json.Unmarshal(persisted.BodyRequirementsJSON, &body); err != nil || !slices.Equal(body, sealed.Policy.Body.RuntimeRequirements) {
		return carrier{}, fmt.Errorf("%w: persisted body requirements mismatch", errExecutionPolicyInvalid)
	}
	var carrier carrier
	setCarrier(&carrier, persisted)
	return carrier, nil
}

func validateCarrier(carrier carrier) (sealed, bool, error) {
	persisted := persistence{
		PolicyJSON: carrier.policyJSON, PolicyHash: carrier.policyHash,
		PolicyVersion: carrier.policyVersion, BodyProtocol: carrier.bodyProtocol,
		SupervisorRequirementsJSON: carrier.supervisorRequirementsJSON,
		SupervisorRequirementsHash: carrier.supervisorRequirementsHash,
		BodyRequirementsJSON:       carrier.bodyRequirementsJSON,
		BodyRequirementsHash:       carrier.bodyRequirementsHash,
	}
	if persistenceIsZero(persisted) {
		return sealed{}, false, nil
	}
	validated, err := carrierFromPersistence(persisted)
	if err != nil {
		return sealed{}, false, err
	}
	sealed, err := decode(validated.policyJSON)
	return sealed, err == nil, err
}

func setCarrier(dst *carrier, persisted persistence) {
	*dst = carrier{
		policyJSON: append([]byte(nil), persisted.PolicyJSON...), policyHash: persisted.PolicyHash,
		policyVersion: persisted.PolicyVersion, bodyProtocol: persisted.BodyProtocol,
		supervisorRequirementsJSON: append([]byte(nil), persisted.SupervisorRequirementsJSON...),
		supervisorRequirementsHash: persisted.SupervisorRequirementsHash,
		bodyRequirementsJSON:       append([]byte(nil), persisted.BodyRequirementsJSON...),
		bodyRequirementsHash:       persisted.BodyRequirementsHash,
	}
}

func cloneCarrier(carrier carrier) carrier {
	carrier.policyJSON = append([]byte(nil), carrier.policyJSON...)
	carrier.supervisorRequirementsJSON = append([]byte(nil), carrier.supervisorRequirementsJSON...)
	carrier.bodyRequirementsJSON = append([]byte(nil), carrier.bodyRequirementsJSON...)
	return carrier
}

func persistenceIsZero(p persistence) bool {
	return len(p.PolicyJSON) == 0 && p.PolicyHash == "" && p.PolicyVersion == 0 && p.BodyProtocol == 0 &&
		len(p.SupervisorRequirementsJSON) == 0 && p.SupervisorRequirementsHash == "" &&
		len(p.BodyRequirementsJSON) == 0 && p.BodyRequirementsHash == ""
}

func persistenceEqual(a, b persistence) bool {
	return bytes.Equal(a.PolicyJSON, b.PolicyJSON) && a.PolicyHash == b.PolicyHash && a.PolicyVersion == b.PolicyVersion &&
		a.BodyProtocol == b.BodyProtocol && bytes.Equal(a.SupervisorRequirementsJSON, b.SupervisorRequirementsJSON) &&
		a.SupervisorRequirementsHash == b.SupervisorRequirementsHash && bytes.Equal(a.BodyRequirementsJSON, b.BodyRequirementsJSON) &&
		a.BodyRequirementsHash == b.BodyRequirementsHash
}

func normalizeExecutionPolicyV1(policy nodeExecutionPolicy) (nodeExecutionPolicy, error) {
	if policy.Version != 1 {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: policy version must be 1", errExecutionPolicyInvalid)
	}
	for field, value := range map[string]string{"pipeline": policy.Pipeline, "node_id": policy.NodeID} {
		if err := validateExactIdentity(field, value); err != nil {
			return nodeExecutionPolicy{}, err
		}
	}
	locations, err := normalizeExactList("allowed location", policy.AllowedLocations)
	policy.AllowedLocations = locations
	if err != nil || len(policy.AllowedLocations) == 0 || len(policy.AllowedLocations) > 2 {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: allowed locations must be a non-empty subset of local and cloud", errExecutionPolicyInvalid)
	}
	for _, location := range policy.AllowedLocations {
		if location != executorLocationLocal && location != executorLocationCloud {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: allowed location %q", errExecutionPolicyInvalid, location)
		}
	}
	if len(policy.Dependencies) > maxExecutionPolicyItems || len(policy.SecretNames) > maxExecutionPolicyItems ||
		len(policy.ArtifactInputs) > maxExecutionPolicyItems || len(policy.ArtifactOutputs) > maxExecutionPolicyItems ||
		len(policy.Actions) > maxExecutionPolicyItems {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: collection exceeds %d items", errExecutionPolicyInvalid, maxExecutionPolicyItems)
	}

	deps := make([]nodeDependencyAuthority, len(policy.Dependencies))
	for i, dep := range policy.Dependencies {
		if err := validateExactIdentity("dependency node_id", dep.NodeID); err != nil {
			return nodeExecutionPolicy{}, err
		}
		if !dep.WholeOutput || dep.MaxBytes == 0 || dep.MaxBytes > maxExecutionEventBytes {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: dependency %q must grant bounded whole-output access", errExecutionPolicyInvalid, dep.NodeID)
		}
		deps[i] = nodeDependencyAuthority{NodeID: dep.NodeID, WholeOutput: true, MaxBytes: dep.MaxBytes}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].NodeID < deps[j].NodeID })
	for i := 1; i < len(deps); i++ {
		if deps[i-1].NodeID == deps[i].NodeID {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: duplicate dependency %q", errExecutionPolicyInvalid, deps[i].NodeID)
		}
	}
	policy.Dependencies = deps

	policy.SecretNames = append([]string(nil), policy.SecretNames...)
	for _, name := range policy.SecretNames {
		if err := secretname.Validate(name); err != nil {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: secret name: %v", errExecutionPolicyInvalid, err)
		}
	}
	sort.Strings(policy.SecretNames)
	for i := 1; i < len(policy.SecretNames); i++ {
		if policy.SecretNames[i-1] == policy.SecretNames[i] {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: duplicate secret name %q", errExecutionPolicyInvalid, policy.SecretNames[i])
		}
	}
	policy.SupervisorRequirements, err = normalizeExactList("supervisor requirement", policy.SupervisorRequirements)
	if err != nil {
		return nodeExecutionPolicy{}, err
	}
	policy.Body.RuntimeRequirements, err = normalizeExactList("body runtime requirement", policy.Body.RuntimeRequirements)
	if err != nil {
		return nodeExecutionPolicy{}, err
	}
	if !slices.Contains(policy.SupervisorRequirements, fleetSupervisorRuntimeRequirement) {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: missing supervisor requirement %q", errExecutionPolicyInvalid, fleetSupervisorRuntimeRequirement)
	}
	if !slices.Contains(policy.Body.RuntimeRequirements, fleetBodyRuntimeRequirement) {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: missing body requirement %q", errExecutionPolicyInvalid, fleetBodyRuntimeRequirement)
	}
	if slices.Contains(policy.SupervisorRequirements, assistedExecutionPolicyRequirement) || slices.Contains(policy.Body.RuntimeRequirements, assistedExecutionPolicyRequirement) {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: store requirement %q is not a runtime requirement", errExecutionPolicyInvalid, assistedExecutionPolicyRequirement)
	}
	if policy.Body.ProtocolVersion != 1 {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: body protocol must be 1", errExecutionPolicyInvalid)
	}
	if policy.Body.Source.Kind != "git" && policy.Body.Source.Kind != "working_tree" && policy.Body.Source.Kind != "artifact" {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: unknown body source kind %q", errExecutionPolicyInvalid, policy.Body.Source.Kind)
	}
	switch policy.Body.Source.Kind {
	case "git":
		if !runspec.IsCommitID(policy.Body.Source.Identity) {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: git source identity must be an immutable lowercase commit", errExecutionPolicyInvalid)
		}
	case "working_tree", "artifact":
		if err := validateDigest("body source identity", policy.Body.Source.Identity); err != nil {
			return nodeExecutionPolicy{}, err
		}
	}
	if err := validateDigest("body source manifest digest", policy.Body.Source.ManifestDigest); err != nil {
		return nodeExecutionPolicy{}, err
	}
	if err := validateDigest("body plan digest", policy.Body.Source.PlanDigest); err != nil {
		return nodeExecutionPolicy{}, err
	}

	inputs := append([]nodeArtifactInput(nil), policy.ArtifactInputs...)
	for i := range inputs {
		if err := validateExactIdentity("artifact producer", inputs[i].ProducerNodeID); err != nil {
			return nodeExecutionPolicy{}, err
		}
		if err := validateDigest("artifact manifest digest", inputs[i].ManifestDigest); err != nil {
			return nodeExecutionPolicy{}, err
		}
		if err := validateRelativeLiteralPath("artifact destination", inputs[i].Into, true); err != nil {
			return nodeExecutionPolicy{}, err
		}
		if inputs[i].MaxBytes == 0 || inputs[i].MaxBytes > maxExecutionOutputBytes || inputs[i].MaxFiles < 1 || inputs[i].MaxFiles > maxExecutionFiles ||
			inputs[i].MaxManifestBytes < 1 || inputs[i].MaxManifestBytes > maxExecutionEventBytes {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: artifact input bounds", errExecutionPolicyInvalid)
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
			return nodeExecutionPolicy{}, fmt.Errorf("%w: duplicate artifact input", errExecutionPolicyInvalid)
		}
	}
	for i := range inputs {
		for j := 0; j < i; j++ {
			if strings.EqualFold(inputs[i].Into, inputs[j].Into) {
				return nodeExecutionPolicy{}, fmt.Errorf("%w: artifact destinations collide on a case-insensitive filesystem", errExecutionPolicyInvalid)
			}
		}
	}

	outputs := append([]nodeArtifactOutput(nil), policy.ArtifactOutputs...)
	for i := range outputs {
		if err := validateRelativeGlob(outputs[i].Glob); err != nil {
			return nodeExecutionPolicy{}, err
		}
		if outputs[i].MaxBytes == 0 || outputs[i].MaxBytes > maxExecutionOutputBytes || outputs[i].MaxFiles < 1 || outputs[i].MaxFiles > maxExecutionFiles {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: output bounds", errExecutionPolicyInvalid)
		}
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Glob < outputs[j].Glob })
	for i := range outputs {
		for j := 0; j < i; j++ {
			if strings.EqualFold(outputs[i].Glob, outputs[j].Glob) {
				return nodeExecutionPolicy{}, fmt.Errorf("%w: duplicate output glob", errExecutionPolicyInvalid)
			}
		}
	}
	policy.ArtifactOutputs = outputs
	if policy.ArtifactOutputMaxBytes > maxExecutionOutputBytes || policy.ResultMaxBytes == 0 || policy.ResultMaxBytes > maxExecutionEventBytes {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: body output bounds", errExecutionPolicyInvalid)
	}

	if len(policy.Emission.Events) > 3 || len(policy.Emission.StepIDs) > maxExecutionPolicyItems ||
		policy.Emission.MaxEvents < 0 || policy.Emission.MaxEvents > maxExecutionEvents ||
		policy.Emission.MaxPayloadBytes < 0 || policy.Emission.MaxPayloadBytes > maxExecutionEventBytes ||
		policy.Emission.MaxTotalBytes < 0 || policy.Emission.MaxTotalBytes > maxExecutionEventBytes*maxExecutionPolicyItems {
		return nodeExecutionPolicy{}, fmt.Errorf("%w: emission bounds", errExecutionPolicyInvalid)
	}
	events := append([]nodeExecutionEventKind(nil), policy.Emission.Events...)
	for _, kind := range events {
		if kind != nodeExecutionEventAnnotation && kind != nodeExecutionEventSummary && kind != nodeExecutionEventLog {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: unknown event kind %q", errExecutionPolicyInvalid, kind)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i] < events[j] })
	events = slices.Compact(events)
	policy.Emission.Events = events
	policy.Emission.StepIDs, err = normalizeExactList("step id", policy.Emission.StepIDs)
	if err != nil {
		return nodeExecutionPolicy{}, err
	}

	actions := append([]nodeExecutionGrant(nil), policy.Actions...)
	for i := range actions {
		if err := normalizeExecutionGrant(&actions[i]); err != nil {
			return nodeExecutionPolicy{}, err
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].GrantID < actions[j].GrantID })
	for i := 1; i < len(actions); i++ {
		if actions[i-1].GrantID == actions[i].GrantID {
			return nodeExecutionPolicy{}, fmt.Errorf("%w: duplicate grant %q", errExecutionPolicyInvalid, actions[i].GrantID)
		}
	}
	policy.Actions = actions
	return policy, nil
}

func normalizeExecutionGrant(grant *nodeExecutionGrant) error {
	if err := validateExactIdentity("grant_id", grant.GrantID); err != nil {
		return err
	}
	present := 0
	for _, ok := range []bool{grant.ToolSlot != nil, grant.Await != nil, grant.Ref != nil} {
		if ok {
			present++
		}
	}
	if present != 1 {
		return fmt.Errorf("%w: grant %q must have exactly one action", errExecutionPolicyInvalid, grant.GrantID)
	}
	switch grant.Kind {
	case nodeExecutionActionToolSlot:
		if grant.ToolSlot != nil && grant.ToolSlot.MaxCost >= 1 && grant.ToolSlot.MaxCost <= grant.ToolSlot.Capacity && grant.ToolSlot.Capacity <= 1_000_000 &&
			(grant.ToolSlot.Scope == "run" || grant.ToolSlot.Scope == "box" || grant.ToolSlot.Scope == "global") &&
			(grant.ToolSlot.OnLimit == "queue" || grant.ToolSlot.OnLimit == "fail" || grant.ToolSlot.OnLimit == "skip" || grant.ToolSlot.OnLimit == "cancel_others") &&
			grant.ToolSlot.QueueTimeoutNanos >= 0 &&
			validateExactIdentity("tool slot group", grant.ToolSlot.Group) == nil {
			return nil
		}
	case nodeExecutionActionAwait:
		if grant.Await != nil && grant.Await.MaxArgs >= 0 && grant.Await.MaxArgs <= maxExecutionPolicyItems && grant.Await.MaxArgBytes >= 0 &&
			grant.Await.MaxArgBytes <= maxExecutionEventBytes && grant.Await.TimeoutNanos >= 0 && grant.Await.MaxInvocations >= 1 &&
			grant.Await.MaxInvocations <= maxExecutionPolicyItems && validateExactIdentity("await pipeline", grant.Await.Pipeline) == nil &&
			validateExactIdentity("await node_id", grant.Await.NodeID) == nil && validateDigest("await args hash", grant.Await.ArgsHash) == nil &&
			validateOptionalExactIdentity("await repo", grant.Await.Repo) == nil && validateOptionalExactIdentity("await branch", grant.Await.Branch) == nil {
			return nil
		}
	case nodeExecutionActionRef:
		if grant.Ref != nil && grant.Ref.MaxAgeNanos >= 0 && grant.Ref.MaxBytes > 0 && grant.Ref.MaxBytes <= maxExecutionEventBytes &&
			validateExactIdentity("ref pipeline", grant.Ref.Pipeline) == nil && validateExactIdentity("ref node_id", grant.Ref.NodeID) == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: grant %q kind/payload mismatch", errExecutionPolicyInvalid, grant.GrantID)
}

func normalizeExactList(field string, values []string) ([]string, error) {
	if len(values) > maxExecutionPolicyItems {
		return nil, fmt.Errorf("%w: %s exceeds %d items", errExecutionPolicyInvalid, field, maxExecutionPolicyItems)
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
			return nil, fmt.Errorf("%w: duplicate %s %q", errExecutionPolicyInvalid, field, out[i])
		}
	}
	return out, nil
}

func validateExactIdentity(field, value string) error {
	if value == "" || len(value) > maxExecutionPolicyStringBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s is not an exact bounded identity", errExecutionPolicyInvalid, field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains a control character", errExecutionPolicyInvalid, field)
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

func cloneExecutionPolicy(policy nodeExecutionPolicy) nodeExecutionPolicy {
	policy.Dependencies = append([]nodeDependencyAuthority(nil), policy.Dependencies...)
	policy.SecretNames = append([]string(nil), policy.SecretNames...)
	policy.ArtifactInputs = append([]nodeArtifactInput(nil), policy.ArtifactInputs...)
	policy.ArtifactOutputs = append([]nodeArtifactOutput(nil), policy.ArtifactOutputs...)
	policy.Emission.Events = append([]nodeExecutionEventKind(nil), policy.Emission.Events...)
	policy.Emission.StepIDs = append([]string(nil), policy.Emission.StepIDs...)
	policy.SupervisorRequirements = append([]string(nil), policy.SupervisorRequirements...)
	policy.Body.RuntimeRequirements = append([]string(nil), policy.Body.RuntimeRequirements...)
	policy.Actions = append([]nodeExecutionGrant(nil), policy.Actions...)
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
	}
	return policy
}

func validateDigest(field, value string) error {
	if err := validateExactIdentity(field, value); err != nil {
		return err
	}
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%w: %s must be a sha256 digest", errExecutionPolicyInvalid, field)
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || value != strings.ToLower(value) {
		return fmt.Errorf("%w: %s must be a sha256 digest", errExecutionPolicyInvalid, field)
	}
	return nil
}

func validateRelativeLiteralPath(field, value string, emptyOK bool) error {
	if value == "" && emptyOK {
		return nil
	}
	if err := validateExactIdentity(field, value); err != nil {
		return err
	}
	if strings.ContainsAny(value, `\\<>:"|?*`) || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%w: %s must be a canonical relative path", errExecutionPolicyInvalid, field)
	}
	for _, component := range strings.Split(value, "/") {
		if !portableLiteralPathComponent(component) {
			return fmt.Errorf("%w: %s contains a non-portable path component %q", errExecutionPolicyInvalid, field, component)
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
		return fmt.Errorf("%w: output glob must be a canonical relative slash path", errExecutionPolicyInvalid)
	}
	if _, err := path.Match(value, "probe"); err != nil {
		return fmt.Errorf("%w: invalid output glob: %v", errExecutionPolicyInvalid, err)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return fmt.Errorf("%w: output glob contains a non-portable path component %q", errExecutionPolicyInvalid, component)
		}
		if !strings.ContainsAny(component, "*?[") {
			if !portableLiteralPathComponent(component) {
				return fmt.Errorf("%w: output glob contains a non-portable path component %q", errExecutionPolicyInvalid, component)
			}
			continue
		}
		if device, matches := globMatchesWindowsDevice(component); matches {
			return fmt.Errorf("%w: output glob can match reserved Windows device %q", errExecutionPolicyInvalid, device)
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

func executionPolicyDepsMatch(stored []string, policy []nodeDependencyAuthority) bool {
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

func validateAssistedPlacement(binding binding, declared []string) error {
	allowed, err := allowedLocationsForBinding(binding)
	if err != nil {
		return err
	}
	if !slices.Equal(allowed, declared) {
		return fmt.Errorf("%w: policy allowed locations %v do not match stored requirements %v", errExecutionPolicyInvalid, declared, allowed)
	}
	return nil
}

// allowedLocationsForBinding derives helper placement from the complete
// persisted scheduling requirement set.
func allowedLocationsForBinding(binding binding) ([]string, error) {
	allowed := []string{executorLocationCloud, executorLocationLocal}
	if binding.RequiredExecutorLocation != "" {
		var err error
		allowed, err = narrowAssistedLocations(allowed, []string{binding.RequiredExecutorLocation})
		if err != nil {
			return nil, err
		}
	}
	for _, term := range binding.NeedsLabels {
		termPlacements, found, err := assistedPlacementFromTerm(term)
		if err != nil {
			return nil, err
		}
		if found {
			allowed, err = narrowAssistedLocations(allowed, termPlacements)
			if err != nil {
				return nil, err
			}
		}
	}
	return allowed, nil
}

func assistedPlacementFromTerm(term string) ([]string, bool, error) {
	var found []string
	sawNonPlacement := false
	for _, alternative := range strings.Split(term, ",") {
		alternative = strings.TrimSpace(alternative)
		if alternative == "" {
			return nil, false, fmt.Errorf("%w: placement term %q has an empty alternative", errExecutionPolicyInvalid, term)
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
			return nil, false, fmt.Errorf("%w: placement term %q mixes placement and non-placement alternatives", errExecutionPolicyInvalid, term)
		}
		if !slices.Contains(found, placement) {
			found = append(found, placement)
		}
	}
	if len(found) > 0 && sawNonPlacement {
		return nil, false, fmt.Errorf("%w: placement term %q mixes placement and non-placement alternatives", errExecutionPolicyInvalid, term)
	}
	if len(found) > 1 {
		return nil, false, fmt.Errorf("%w: placement term %q is ambiguous", errExecutionPolicyInvalid, term)
	}
	return found, len(found) != 0, nil
}

func narrowAssistedLocations(current, required []string) ([]string, error) {
	for _, location := range required {
		if location == executorLocationCoordinator || location == executorLocationUnknown {
			return nil, fmt.Errorf("%w: assisted execution cannot target %s", errExecutionPolicyInvalid, location)
		}
		if location != executorLocationLocal && location != executorLocationCloud {
			return nil, fmt.Errorf("%w: unknown stored placement %q", errExecutionPolicyInvalid, location)
		}
	}
	var out []string
	for _, location := range current {
		if slices.Contains(required, location) {
			out = append(out, location)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: stored placement requirements conflict", errExecutionPolicyInvalid)
	}
	return out, nil
}
