package store_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func seedMatchNode(t *testing.T, s *store.Store, runID, repoURL string, cores float64, needs ...string) {
	t.Helper()
	ctx := context.Background()
	seedRepoTrigger(t, s, runID, repoURL, time.Now())
	plan := `{"nodes":[{"id":"work","modifiers":{"res_cores":` + strconv.FormatFloat(cores, 'f', -1, 64) + `}}]}`
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, `UPDATE runs SET plan_json = ? WHERE id = ?`), plan, runID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: "work", Status: "pending", NeedsLabels: needs,
		RequestedCores: cores,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, runID, "work"); err != nil {
		t.Fatal(err)
	}
}

// A legacy runner that reports its capacity is never handed a node that asks
// for more, and one that reports none claims as it always has.
func TestQueueClaimNeverTakesANodeLargerThanTheRunnersCapacity(t *testing.T) {
	s := storetest.Open(t)
	seedMatchNode(t, s, "run-big", "", 4)
	small := store.WithClaimProfile(context.Background(), match.Profile{Capacity: match.Resources{Cores: 2}})
	if _, err := s.ClaimNextReadyNode(small, store.ClaimIdentity{}, "pi:1", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a 2-core runner's claim of a 4-core node = %v, want not found", err)
	}
	big := store.WithClaimProfile(context.Background(), match.Profile{Capacity: match.Resources{Cores: 8}})
	if n, err := s.ClaimNextReadyNode(big, store.ClaimIdentity{}, "desk:1", time.Minute, nil); err != nil || n.RunID != "run-big" {
		t.Fatalf("an 8-core runner's claim = %+v, %v; want run-big", n, err)
	}
}

// A runner cannot name itself: name= answers to the token's principal, never
// to a label the claim asserts.
func TestQueueClaimBindsNameToTheTokenPrincipal(t *testing.T) {
	s := storetest.Open(t)
	seedMatchNode(t, s, "run", "", 1, "name=moonborn")
	impostor := store.ClaimIdentity{Principal: "agent:laptop"}
	if _, err := s.ClaimNextReadyNode(context.Background(), impostor, "laptop:1", time.Minute,
		[]string{"name=moonborn"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a runner asserting name=moonborn = %v, want not found", err)
	}
	owner := store.ClaimIdentity{Principal: "agent:moonborn"}
	if n, err := s.ClaimNextReadyNode(context.Background(), owner, "moonborn:1", time.Minute, nil); err != nil || n.RunID != "run" {
		t.Fatalf("the runner whose token is moonborn = %+v, %v; want the node", n, err)
	}
}

// A named claim answers to the claimant's accept list and to the node's
// selector against the labels the claim sent.
func TestNamedClaimAppliesTheClaimantsProfile(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedMatchNode(t, s, "run-b", "https://github.com/acme/b.git", 1, "gpu")
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/a"})
	if err != nil {
		t.Fatal(err)
	}
	refusing := store.WithClaimProfile(ctx, match.Profile{Accept: allow})
	if _, err := s.ClaimNamedNode(refusing, store.ClaimIdentity{}, "run-b", "work", "k8s-job:1", time.Minute,
		store.NamedClaimOptions{Labels: []string{"gpu"}}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("a named claim whose list refuses the repository = %v, want held", err)
	}
	if _, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-b", "work", "k8s-job:2", time.Minute,
		store.NamedClaimOptions{}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("a named claim without gpu = %v, want held", err)
	}
	if n, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-b", "work", "k8s-job:3", time.Minute,
		store.NamedClaimOptions{Labels: []string{"gpu"}}); err != nil || n.ClaimedBy != "k8s-job:3" {
		t.Fatalf("a named claim with gpu = %+v, %v; want the node", n, err)
	}
}

// Agent names are unique within a team, so a node selector or workload
// identity bound to a name admits one machine.
func TestRunnerTokenRefusesADuplicateAgentName(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	seedMinter(t, s, store.DefaultTeam, "owner", store.RoleOwner)
	now := time.Now().UTC()
	_, first, err := tenant.CreateMachineToken(ctx, "moonborn", []string{"nodes.claim"}, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tenant.CreateMachineToken(ctx, "moonborn", []string{"nodes.claim"}, "owner", now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a second moonborn token = %v, want ErrAgentNameTaken", err)
	}
	if _, _, err := tenant.CreateMachineToken(ctx, "pi", []string{"nodes.claim"}, "owner", now); err != nil {
		t.Fatalf("another name = %v", err)
	}
	if err := tenant.RevokeTeamToken(ctx, first.Prefix, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tenant.CreateMachineToken(ctx, "moonborn", []string{"nodes.claim"}, "owner", now.Add(time.Second)); err != nil {
		t.Fatalf("re-minting moonborn after revoking it = %v", err)
	}
}

// Metering is billing, not placement trust: a metered agent's named claim
// still answers to the repository list it sent.
func TestNamedClaimOfAMeteredAgentObeysItsAcceptList(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:pool")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_1", "admin"); err != nil {
		t.Fatal(err)
	}
	seedMatchNode(t, s, "run-b", "https://github.com/acme/b.git", 1)
	allow, err := match.ParseAccept([]string{"github.com/acme/a"})
	if err != nil {
		t.Fatal(err)
	}
	refusing := store.WithClaimProfile(ctx, match.Profile{Accept: allow})
	if _, err := s.ClaimNamedNode(refusing, claimant, "run-b", "work", "k8s-job:1", time.Minute,
		store.NamedClaimOptions{}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("a metered named claim whose list refuses the repository = %v, want held", err)
	}
}

// The operator's metered pool claims a location=cloud node by name without
// sending labels, as it did before the named claim checked selectors, while
// the queue claim still never grants a runner a location.
func TestCloudPlacementMatchesThePreChangeBehavior(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, s, "agent:pool")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_1", "admin"); err != nil {
		t.Fatal(err)
	}
	seedMatchNode(t, s, "run-cloud", "", 1, "location=cloud")
	if _, err := s.ClaimNextReadyNode(ctx, pool, "pool:1", time.Minute, []string{"location=cloud"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a queue claim asserting location=cloud = %v, want not found", err)
	}
	if n, err := s.ClaimNamedNode(ctx, pool, "run-cloud", "work", "k8s-job:1", time.Minute,
		store.NamedClaimOptions{}); err != nil || n.ClaimedBy != "k8s-job:1" {
		t.Fatalf("the pool's named claim of a location=cloud node = %+v, %v; want the node", n, err)
	}
	seedMatchNode(t, s, "run-other", "", 1, "location=cloud")
	if _, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-other", "work", "k8s-job:2", time.Minute,
		store.NamedClaimOptions{Labels: []string{"location=cloud"}}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("an unmetered dispatcher asserting location=cloud = %v, want held", err)
	}
}

// A dispatcher older than the labels field sends none: it still claims an
// unlabeled node, and a labeled one is refused rather than admitted unchecked.
func TestNamedClaimFromADispatcherThatSendsNoLabels(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedMatchNode(t, s, "run-plain", "", 1)
	seedMatchNode(t, s, "run-gpu", "", 1, "gpu")
	if _, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-plain", "work", "k8s-job:1", time.Minute,
		store.NamedClaimOptions{}); err != nil {
		t.Fatalf("an unlabeled node = %v", err)
	}
	_, err := s.ClaimNamedNode(ctx, store.ClaimIdentity{}, "run-gpu", "work", "k8s-job:2", time.Minute, store.NamedClaimOptions{})
	if !errors.Is(err, store.ErrLockHeld) || !strings.Contains(err.Error(), "sent no labels") {
		t.Fatalf("a gpu node from a claim that sent no labels = %v, want a refusal naming the missing labels", err)
	}
}

// Every mint path refuses a second live token for an agent name; rotation is
// the one overlap, with the predecessor it revokes at the end of its grace.
func TestAgentNameIsUniqueOnEveryMintPath(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now().UTC()
	_, first, err := s.CreateToken("agent:moonborn", store.TokenKindRunner, []string{"nodes.claim"}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken("agent:moonborn", store.TokenKindRunner, []string{"nodes.claim"}, 0, now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a second moonborn token through CreateToken = %v, want ErrAgentNameTaken", err)
	}
	if _, _, err := s.CreateToken("agent:moonborn", store.TokenKindUser, []string{"runs.read"}, 0, now); err != nil {
		t.Fatalf("a user token that shares the principal = %v", err)
	}
	if _, _, _, err := s.RotateToken(first.Prefix, time.Hour, 0, now); err != nil {
		t.Fatalf("rotating moonborn = %v", err)
	}
	if _, _, err := s.CreateToken("agent:moonborn", store.TokenKindRunner, []string{"nodes.claim"}, 0, now); !errors.Is(err, store.ErrAgentNameTaken) {
		t.Fatalf("a mint during the rotation grace = %v, want ErrAgentNameTaken", err)
	}
}
