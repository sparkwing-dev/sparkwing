package store

import (
	"cmp"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type storeLockTier string

const (
	lockGlobal       storeLockTier = "global advisories"
	lockLease        storeLockTier = "singleton leases"
	lockSettings     storeLockTier = "settings and signup gates"
	lockTeam         storeLockTier = "teams"
	lockAccount      storeLockTier = "accounts"
	lockMembership   storeLockTier = "memberships"
	lockAuthority    storeLockTier = "authority"
	lockInstallation storeLockTier = "GitHub installations"
	lockCredential   storeLockTier = "subscriptions and credentials"
	lockSchedule     storeLockTier = "schedules and intake"
	lockTrigger      storeLockTier = "trigger owners"
	lockEval         storeLockTier = "eval owners"
	lockRun          storeLockTier = "runs"
	lockNode         storeLockTier = "nodes"
	lockConcurrency  storeLockTier = "concurrency entries, holders and waiters"
	lockUpload       storeLockTier = "upload tickets"
	lockDigest       storeLockTier = "blob digest advisories"
	lockQuota        storeLockTier = "quota dimensions"
	lockBilling      storeLockTier = "billing and counters"
	lockRecord       storeLockTier = "records and metadata"
)

var storeLockOrder = [...]storeLockTier{
	lockGlobal, lockLease, lockSettings, lockTeam, lockAccount, lockMembership,
	lockAuthority, lockInstallation, lockCredential, lockSchedule, lockTrigger,
	lockEval, lockRun, lockNode, lockConcurrency, lockUpload, lockDigest,
	lockQuota, lockBilling, lockRecord,
}

func compareLockTiers(a, b storeLockTier) int {
	return cmp.Compare(slices.Index(storeLockOrder[:], a), slices.Index(storeLockOrder[:], b))
}

type authorityLock struct {
	ref   capability.AuthorityRef
	depth int
	write bool
}

func compareAuthorityLocks(a, b authorityLock) int {
	return compareStoreLocks(authorityLockKey(a), authorityLockKey(b))
}

func authorityLockKey(a authorityLock) storeLockKey {
	return storeLockKey{tier: lockAuthority, depth: a.depth, keys: [3]string{string(a.ref.Kind), a.ref.ID}}
}

type storeLockKey struct {
	tier       storeLockTier
	depth      int
	keys       [3]string
	generation int64
}

func compareStoreLocks(a, b storeLockKey) int {
	if order := cmp.Or(compareLockTiers(a.tier, b.tier), cmp.Compare(b.depth, a.depth)); order != 0 {
		return order
	}
	for i := range a.keys {
		if order := strings.Compare(a.keys[i], b.keys[i]); order != 0 {
			return order
		}
	}
	return cmp.Compare(a.generation, b.generation)
}
