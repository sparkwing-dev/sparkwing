package store

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storeclock"
)

// Option configures a Store inside Open. Its function is unexported, so an
// option cannot be applied to a store that is already serving.
type Option struct {
	apply    func(*Store)
	policies func(*Store) (Policies, error)
}

// Policies holds dependencies selected once, before Open publishes the store.
type Policies struct {
	Intake         IntakePolicy
	Console        func(capability.Principal, time.Time) bool
	ClaimProfiles  ClaimProfileResolver
	PlanSecrets    PlanSecretOpener
	SessionKeys    SessionKeyCipher
	RunEnvironment SessionKeyCipher
	ResultBlobs    BlobManifestReader
}

// WithPolicies assembles dependencies that need the store being opened. The
// factory must not publish that store before it returns.
func WithPolicies(factory func(*Store) (Policies, error)) Option {
	return Option{policies: factory}
}

func (s *Store) configurePolicies(policies Policies) {
	if policies.Intake != nil {
		s.intakePolicy = policies.Intake
	}
	if policies.Console != nil {
		s.consolePolicy = policies.Console
	}
	if policies.ClaimProfiles != nil {
		s.claimProfiles = policies.ClaimProfiles
	}
	if policies.PlanSecrets != nil {
		s.planSecretOpener = policies.PlanSecrets
	}
	if policies.SessionKeys != nil {
		s.csrfCipher = policies.SessionKeys
	}
	if policies.RunEnvironment != nil {
		s.runEnvCipher = policies.RunEnvironment
	}
	if policies.ResultBlobs != nil {
		records := &blobRecords{manifests: policies.ResultBlobs}
		s.resultRecords, s.materializations = records, records
	}
}

func init() {
	storeclock.Now = func(s any) time.Time { return s.(*Store).now() }
	storeclock.Option = func(clock func() time.Time) any {
		return Option{apply: func(s *Store) { s.clock, s.pinnedClock = clock, s.now }}
	}
}
