package executionpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
)

const (
	FleetSupervisorRequirement = "fleet-supervisor-v1"
	FleetBodyRequirement       = "fleet-body-v1"

	maxRuntimeRequirements          = 64
	MaxRuntimeRequirementsJSONBytes = 16 << 10
	MaxRuntimeIdentityJSONBytes     = 4 << 10
	maxRuntimeProtocol              = 65535
)

type runtimeRequirementFloor struct {
	scope, release string
}

var runtimeRequirementFloors = map[string]runtimeRequirementFloor{
	FleetSupervisorRequirement: {scope: "supervisor", release: "v0.41.0"},
	FleetBodyRequirement:       {scope: "body_host", release: "v0.41.0"},
}

var bodyProtocolRelease = map[int]string{
	AssistedBodyProtocolVersion: "v0.41.0",
}

var ErrBodyAttestationRequired = errors.New("compiled body attestation required")

// UpgradeRequiredError identifies host capabilities an executor must gain
// before it can prepare an assisted node.
type UpgradeRequiredError struct {
	Scope          string
	Missing        []string
	MinimumRelease string
	SafeHold       bool
}

func (e *UpgradeRequiredError) Error() string {
	if e == nil {
		return ErrExecutionUpgradeRequired.Error()
	}
	if e.SafeHold {
		return fmt.Sprintf("%s: %s capability floor is unresolved; hold for a compatible release", ErrExecutionUpgradeRequired, e.Scope)
	}
	if len(e.Missing) == 0 {
		return fmt.Sprintf("%s: %s requires %s", ErrExecutionUpgradeRequired, e.Scope, e.MinimumRelease)
	}
	return fmt.Sprintf("%s: %s lacks %s (introduced by %s)", ErrExecutionUpgradeRequired, e.Scope,
		strings.Join(e.Missing, ","), e.MinimumRelease)
}

func (e *UpgradeRequiredError) Unwrap() error { return ErrExecutionUpgradeRequired }

// ProtocolIncompatibleError means the helper only supports protocols newer
// than the sealed workload. Upgrading the controller cannot widen that seal.
type ProtocolIncompatibleError struct {
	PolicyProtocol int
	HelperMinimum  int
	HelperMaximum  int
}

func (e *ProtocolIncompatibleError) Error() string {
	return fmt.Sprintf("%s: policy=%d helper=%d..%d", ErrExecutionProtocolMismatch,
		e.PolicyProtocol, e.HelperMinimum, e.HelperMaximum)
}

func (e *ProtocolIncompatibleError) Unwrap() error { return ErrExecutionProtocolMismatch }

// BodyAttestationRequiredError identifies the sealed candidate that cannot be
// offered until its exact compiled artifact proves its body capabilities.
type BodyAttestationRequiredError struct {
	RunID  string
	NodeID string
}

func (e *BodyAttestationRequiredError) Error() string {
	return fmt.Sprintf("%s for %s/%s", ErrBodyAttestationRequired, e.RunID, e.NodeID)
}

func (e *BodyAttestationRequiredError) Unwrap() error { return ErrBodyAttestationRequired }

// RuntimeReport is an enrolled supervisor's observational heartbeat state.
// Body requirements describe protocols the host can supervise; they never
// attest that a particular compiled workload implements them.
type RuntimeReport struct {
	BodyProtocolMinimum int
	BodyProtocolMaximum int
	Supervisor          []string
	BodyHost            []string
	Build               buildinfo.Identity
}

// CurrentRuntimeReport returns the capabilities implemented by this runner
// supervisor. Workload attestation remains a separate per-candidate step.
func CurrentRuntimeReport(identity buildinfo.Identity) RuntimeReport {
	return RuntimeReport{
		BodyProtocolMinimum: AssistedBodyProtocolVersion,
		BodyProtocolMaximum: AssistedBodyProtocolVersion,
		Supervisor:          []string{FleetSupervisorRequirement},
		BodyHost:            []string{FleetBodyRequirement},
		Build:               identity,
	}
}

// NormalizeRuntimeReport validates and owns one heartbeat report.
func NormalizeRuntimeReport(report RuntimeReport) (RuntimeReport, error) {
	if report.BodyProtocolMinimum < 0 || report.BodyProtocolMaximum < report.BodyProtocolMinimum ||
		report.BodyProtocolMaximum > maxRuntimeProtocol {
		return RuntimeReport{}, fmt.Errorf("invalid executor body protocol range %d..%d", report.BodyProtocolMinimum, report.BodyProtocolMaximum)
	}
	var err error
	if report.Supervisor, err = normalizeRuntimeRequirements(report.Supervisor); err != nil {
		return RuntimeReport{}, fmt.Errorf("supervisor requirements: %w", err)
	}
	if report.BodyHost, err = normalizeRuntimeRequirements(report.BodyHost); err != nil {
		return RuntimeReport{}, fmt.Errorf("body host requirements: %w", err)
	}
	if err := validateBuildIdentity(report.Build); err != nil {
		return RuntimeReport{}, err
	}
	return report, nil
}

func normalizeRuntimeRequirements(values []string) ([]string, error) {
	if len(values) > maxRuntimeRequirements {
		return nil, fmt.Errorf("too many runtime requirements")
	}
	values = append([]string(nil), values...)
	for _, value := range values {
		if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("invalid runtime requirement %q", value)
		}
		for _, r := range value {
			if unicode.IsControl(r) || !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '-' || r == '.') {
				return nil, fmt.Errorf("invalid runtime requirement %q", value)
			}
		}
	}
	sort.Strings(values)
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return nil, fmt.Errorf("duplicate runtime requirement %q", values[i])
		}
	}
	return values, nil
}

func validateBuildIdentity(identity buildinfo.Identity) error {
	raw, err := json.Marshal(identity)
	if err != nil || len(raw) > MaxRuntimeIdentityJSONBytes {
		return errors.New("invalid runner build identity")
	}
	for name, value := range map[string]string{
		"binary": identity.Binary, "version": identity.Version, "commit": identity.Commit,
		"revision_time": identity.RevisionTime, "goos": identity.GOOS, "goarch": identity.GOARCH,
	} {
		if len(value) > 1024 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
			return fmt.Errorf("invalid runner build identity %s", name)
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("invalid runner build identity %s", name)
			}
		}
	}
	return nil
}

// CheckRuntimeCompatibility applies the stable refusal order for a sealed
// policy before a helper reserves local capacity.
func CheckRuntimeCompatibility(policy NodeExecutionPolicy, report RuntimeReport) error {
	report, err := NormalizeRuntimeReport(report)
	if err != nil {
		return unresolvedUpgrade("supervisor", policy.SupervisorRequirements)
	}
	missingSupervisor := missingRuntimeRequirements(policy.SupervisorRequirements, report.Supervisor)
	protocolFloor := 0
	if report.BodyProtocolMinimum == 0 || report.BodyProtocolMaximum == 0 || report.BodyProtocolMaximum < policy.Body.ProtocolVersion {
		protocolFloor = policy.Body.ProtocolVersion
	}
	if len(missingSupervisor) != 0 || protocolFloor != 0 {
		return runtimeUpgrade("supervisor", missingSupervisor, protocolFloor)
	}
	if report.BodyProtocolMinimum > policy.Body.ProtocolVersion {
		return &ProtocolIncompatibleError{
			PolicyProtocol: policy.Body.ProtocolVersion,
			HelperMinimum:  report.BodyProtocolMinimum,
			HelperMaximum:  report.BodyProtocolMaximum,
		}
	}
	if missing := missingRuntimeRequirements(policy.Body.RuntimeRequirements, report.BodyHost); len(missing) != 0 {
		return runtimeUpgrade("body_host", missing, 0)
	}
	return nil
}

// CheckRuntimeCompatibilityMetadata validates the bounded requirement tuple
// used to filter candidates before the full policy document is decoded.
func CheckRuntimeCompatibilityMetadata(policyVersion, bodyProtocol int, supervisorJSON []byte, supervisorHash string,
	bodyJSON []byte, bodyHash string, report RuntimeReport,
) error {
	if policyVersion != NodeExecutionPolicyVersion || bodyProtocol < 1 {
		return fmt.Errorf("%w: unsupported policy metadata version %d protocol %d",
			ErrExecutionUpgradeRequired, policyVersion, bodyProtocol)
	}
	supervisor, err := decodeRuntimeRequirements(supervisorJSON)
	if err != nil || stringListHash(supervisor) != supervisorHash {
		return fmt.Errorf("%w: invalid supervisor requirement metadata", ErrExecutionPolicyInvalid)
	}
	body, err := decodeRuntimeRequirements(bodyJSON)
	if err != nil || stringListHash(body) != bodyHash {
		return fmt.Errorf("%w: invalid body requirement metadata", ErrExecutionPolicyInvalid)
	}
	return CheckRuntimeCompatibility(NodeExecutionPolicy{
		SupervisorRequirements: supervisor,
		Body: NodeCompiledBodyAuthority{
			ProtocolVersion: bodyProtocol, RuntimeRequirements: body,
		},
	}, report)
}

func decodeRuntimeRequirements(raw []byte) ([]string, error) {
	if len(raw) == 0 || len(raw) > MaxRuntimeRequirementsJSONBytes {
		return nil, errors.New("invalid runtime requirement document size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var values []string
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing runtime requirement JSON")
	}
	return normalizeRuntimeRequirements(values)
}

func runtimeUpgrade(scope string, missing []string, protocol int) *UpgradeRequiredError {
	missing = uniqueSorted(missing)
	releases := make([]string, 0, len(missing)+1)
	for _, requirement := range missing {
		floor, ok := runtimeRequirementFloors[requirement]
		if !ok || floor.scope != scope || !buildinfo.IsReleaseVersion(floor.release) {
			return unresolvedUpgrade(scope, missing)
		}
		releases = append(releases, floor.release)
	}
	if protocol != 0 {
		release, ok := bodyProtocolRelease[protocol]
		if !ok || !buildinfo.IsReleaseVersion(release) {
			return unresolvedUpgrade(scope, missing)
		}
		releases = append(releases, release)
	}
	minimum := ""
	for _, release := range releases {
		if minimum == "" || semver.Compare(release, minimum) > 0 {
			minimum = release
		}
	}
	return &UpgradeRequiredError{Scope: scope, Missing: missing, MinimumRelease: minimum}
}

func unresolvedUpgrade(scope string, missing []string) *UpgradeRequiredError {
	return &UpgradeRequiredError{Scope: scope, Missing: uniqueSorted(missing), SafeHold: true}
}

func missingRuntimeRequirements(required, supported []string) []string {
	set := make(map[string]struct{}, len(supported))
	for _, value := range supported {
		set[value] = struct{}{}
	}
	missing := make([]string, 0)
	for _, value := range required {
		if _, ok := set[value]; !ok {
			missing = append(missing, value)
		}
	}
	return uniqueSorted(missing)
}

func uniqueSorted(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return slices.Compact(values)
}

// ClaimBinding binds prepare and offer to the exact private execution seal.
type ClaimBinding struct {
	RunID                      string
	NodeID                     string
	PolicyHash                 string
	PolicyVersion              int
	BodyProtocol               int
	SupervisorRequirementsHash string
	BodyRequirementsHash       string
}

func ClaimBindingOf(owner carrierOwner, runID, nodeID string) (ClaimBinding, error) {
	persisted, err := PersistenceOf(owner)
	if err != nil {
		return ClaimBinding{}, err
	}
	if persistenceIsZero(persisted) {
		return ClaimBinding{}, nil
	}
	return ClaimBinding{
		RunID: runID, NodeID: nodeID, PolicyHash: persisted.PolicyHash,
		PolicyVersion: persisted.PolicyVersion, BodyProtocol: persisted.BodyProtocol,
		SupervisorRequirementsHash: persisted.SupervisorRequirementsHash,
		BodyRequirementsHash:       persisted.BodyRequirementsHash,
	}, nil
}

func (b ClaimBinding) IsZero() bool {
	return b == (ClaimBinding{})
}

func (b ClaimBinding) Validate() error {
	if b.IsZero() {
		return nil
	}
	if b.RunID == "" || b.NodeID == "" || b.PolicyHash == "" || b.PolicyVersion < 1 || b.BodyProtocol < 1 ||
		b.SupervisorRequirementsHash == "" || b.BodyRequirementsHash == "" {
		return fmt.Errorf("%w: partial claim binding", ErrExecutionPolicyInvalid)
	}
	return nil
}

type claimBindingWire struct {
	RunID                      string `json:"run_id"`
	NodeID                     string `json:"node_id"`
	PolicyHash                 string `json:"policy_hash"`
	PolicyVersion              int    `json:"policy_version"`
	BodyProtocol               int    `json:"body_protocol"`
	SupervisorRequirementsHash string `json:"supervisor_requirements_hash"`
	BodyRequirementsHash       string `json:"body_requirements_hash"`
}

func EncodeClaimBinding(binding ClaimBinding) ([]byte, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if binding.IsZero() {
		return nil, nil
	}
	return json.Marshal(claimBindingWire(binding))
}

func DecodeClaimBinding(raw []byte) (ClaimBinding, error) {
	if len(raw) == 0 {
		return ClaimBinding{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire claimBindingWire
	if err := dec.Decode(&wire); err != nil {
		return ClaimBinding{}, fmt.Errorf("%w: decode claim binding: %v", ErrExecutionPolicyInvalid, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ClaimBinding{}, fmt.Errorf("%w: trailing claim binding JSON", ErrExecutionPolicyInvalid)
	}
	binding := ClaimBinding(wire)
	return binding, binding.Validate()
}

type preparationSink struct {
	mu      sync.Mutex
	binding ClaimBinding
}

// PreparationSink owns the private execution binding returned by one prepare
// request. It is intentionally useful only through context helpers.
type PreparationSink struct{ inner *preparationSink }

func NewPreparationSink() *PreparationSink { return &PreparationSink{inner: &preparationSink{}} }

func (s *PreparationSink) Store(binding ClaimBinding) error {
	if s == nil || s.inner == nil {
		return errors.New("execution preparation sink is nil")
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	s.inner.binding = binding
	return nil
}

func (s *PreparationSink) Load() ClaimBinding {
	if s == nil || s.inner == nil {
		return ClaimBinding{}
	}
	s.inner.mu.Lock()
	defer s.inner.mu.Unlock()
	return s.inner.binding
}

type contextKey uint8

const (
	runtimeReportKey contextKey = iota + 1
	preparationSinkKey
	offerBindingKey
	assistedReadyKey
)

func WithRuntimeReport(ctx context.Context, report RuntimeReport) (context.Context, error) {
	normalized, err := NormalizeRuntimeReport(report)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, runtimeReportKey, normalized), nil
}

func RuntimeReportFromContext(ctx context.Context) (RuntimeReport, bool) {
	report, ok := ctx.Value(runtimeReportKey).(RuntimeReport)
	report.Supervisor = append([]string(nil), report.Supervisor...)
	report.BodyHost = append([]string(nil), report.BodyHost...)
	return report, ok
}

func WithPreparationSink(ctx context.Context, sink *PreparationSink) context.Context {
	return context.WithValue(ctx, preparationSinkKey, sink)
}

func StorePreparation(ctx context.Context, binding ClaimBinding) error {
	sink, ok := ctx.Value(preparationSinkKey).(*PreparationSink)
	if !ok || sink == nil {
		if binding.IsZero() {
			return nil
		}
		return errors.New("sealed executor preparation has no private binding sink")
	}
	return sink.Store(binding)
}

func WithOfferBinding(ctx context.Context, binding ClaimBinding) (context.Context, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, offerBindingKey, binding), nil
}

func OfferBindingFromContext(ctx context.Context) (ClaimBinding, bool) {
	binding, ok := ctx.Value(offerBindingKey).(ClaimBinding)
	return binding, ok
}

// WithAssistedReady marks a controller-owned readiness transition as eligible
// for policy construction from durable store facts.
func WithAssistedReady(ctx context.Context) context.Context {
	return context.WithValue(ctx, assistedReadyKey, true)
}

func AssistedReadyFromContext(ctx context.Context) bool {
	requested, _ := ctx.Value(assistedReadyKey).(bool)
	return requested
}
