package store

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func durableSecretStore(t *testing.T, path string, cipher SessionKeyCipher) *Store {
	t.Helper()
	s, err := Open(path, WithPolicies(func(*Store) (Policies, error) { return Policies{LocalSecrets: cipher}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func durableSecretCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher(bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDurableLocalSecretsRestartAndAwardIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	cipher := durableSecretCipher(t)
	s := durableSecretStore(t, path, cipher)
	const value = "requester-private-restart-secret"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	tenant, award := localSecretFixture(t, value, s)
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: award.Principal.Authority, CapabilityPrefix: award.Principal.Capability.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(award.Binding.RunID), Holder: award.Binding.Holder, Generation: runspec.ClaimGeneration(award.Binding.Generation)}}}
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(award.Binding.RunID), To: runstate.RunRunning, Cause: runstate.RunStart, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	now := s.now()
	if _, err := tenant.ReadSecret(t.Context(), award); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = durableSecretStore(t, path, cipher)
	s.clock = func() time.Time { return now }
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadSecret(t.Context(), award)
	if err != nil || row == nil || row.Value != value {
		t.Fatal("restart lost award-bound secret")
	}
	for _, attack := range []string{"team", "run", "name", "authority", "generation"} {
		forged := award
		switch attack {
		case "team":
			forged.Target.Team = "b"
		case "run":
			forged.Target.RunID = "other"
		case "name":
			forged.Target.SecretName = "UNDECLARED"
		case "authority":
			forged.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}
		case "generation":
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET generation=generation+1 WHERE id=?`, award.Binding.RunID)
		}
		row, err := tenant.ReadSecret(t.Context(), forged)
		if err == nil || row != nil || strings.Contains(err.Error(), value) {
			t.Fatalf("%s escaped award boundary", attack)
		}
	}
	rewriteSchemaExec(t, s.db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	backup := filepath.Join(t.TempDir(), "backup.db")
	rewriteSchemaExec(t, s.db, `VACUUM INTO ?`, backup)
	for _, file := range []string{path, backup} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(value)) {
			t.Fatal("plaintext secret in database or backup")
		}
	}
	if strings.Contains(logs.String(), value) {
		t.Fatal("plaintext secret in logs")
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM local_run_secrets`)
	err = s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := localSecretTx(t.Context(), tx, "a", award.Binding.RunID, "KEY")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "submit a new run") {
		t.Fatal("missing durable secret did not explain recovery")
	}
}

func TestDurableLocalSecretsTerminalCleanupIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s := durableSecretStore(t, path, durableSecretCipher(t))
	tenant, a := localSecretFixture(t, "terminal-private-value", s)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1 WHERE run_id=?`, a.Binding.RunID)
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: a.Principal.Authority, CapabilityPrefix: a.Principal.Capability.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(a.Binding.RunID), Holder: a.Binding.Holder, Generation: runspec.ClaimGeneration(a.Binding.Generation)}}}
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(a.Binding.RunID), To: runstate.RunRunning, Cause: runstate.RunStart, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	finish := runstate.FinishRunRequest{Unit: actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(a.Binding.RunID), Cause: runstate.RunFinish, Actor: actor}}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_local_finish BEFORE UPDATE ON triggers BEGIN SELECT RAISE(ABORT,'failed'); END`)
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
		t.Fatal("finish committed despite failed owner write")
	}
	rewriteSchemaCount(t, s.db, "local_run_secrets", 1)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_local_finish`)
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "local_run_secrets", 0)
	if row, err := tenant.ReadSecret(t.Context(), a); err == nil || row != nil {
		t.Fatal("finished award released secret")
	}
}

type refusedLocalCipher struct{ SessionKeyCipher }

func (c refusedLocalCipher) SealBound(secrets.Purpose, string, string, string, bool, bool, string) (string, error) {
	return "", errors.New("cipher failure containing sensitive-private-value")
}

func TestDurableLocalSecretsIntakeFailureAndReplay(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "seal failure"}[fail], func(t *testing.T) {
			var cipher SessionKeyCipher = durableSecretCipher(t)
			if fail {
				cipher = refusedLocalCipher{cipher}
			}
			s := durableSecretStore(t, filepath.Join(t.TempDir(), "state.db"), cipher)
			tenant, request, _ := intakeFixture(t, s)
			request.IdempotencyKey = "local"
			request.Request.LocalSecretNames = []string{"KEY"}
			request.LocalSecrets = map[string]string{"KEY": "sensitive-private-value"}
			first, err := tenant.AdmitRun(t.Context(), request)
			if fail {
				if err == nil || strings.Contains(err.Error(), request.LocalSecrets["KEY"]) {
					t.Fatal("intake exposed cipher error or accepted unavailable encryption")
				}
				rewriteSchemaCount(t, s.db, "runs", 0)
				rewriteSchemaCount(t, s.db, "local_run_secrets", 0)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s.localSecrets.Clear()
			replay, err := tenant.AdmitRun(t.Context(), request)
			if err != nil || !replay.Replayed || replay.RunID != first.RunID {
				t.Fatal("durable replay failed")
			}
			request.LocalSecrets["KEY"] = "different-private-value"
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil || strings.Contains(err.Error(), request.LocalSecrets["KEY"]) {
				t.Fatal("replay replaced secrets or exposed changed value")
			}
			request.LocalSecrets["KEY"] = "sensitive-private-value"
			rewriteSchemaExec(t, s.db, `DELETE FROM local_run_secrets`)
			if _, err := tenant.AdmitRun(t.Context(), request); err == nil {
				t.Fatal("replay restored missing secrets")
			}
		})
	}
}

var _ SessionKeyCipher = refusedLocalCipher{}

func TestDurableLocalSecretsRemainUntilRunOrTeamEnds(t *testing.T) {
	s := durableSecretStore(t, filepath.Join(t.TempDir(), "state.db"), durableSecretCipher(t))
	tenant, award := localSecretFixture(t, "requeue-private-value", s)
	now := s.now().Add(time.Minute)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET lease_expires_at=? WHERE id=?`, now.UnixNano(), award.Binding.RunID)
	s.clock = func() time.Time { return now }
	reaped, err := s.AsOperator().ReapUnits(t.Context(), 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatal("owner did not requeue")
	}
	rewriteSchemaCount(t, s.db, "local_run_secrets", 1)
	if row, err := tenant.ReadSecret(t.Context(), award); err == nil || row != nil {
		t.Fatal("expired award released durable secrets")
	}
	if _, _, err := s.AsOperator().RequestTeamDeletion(t.Context(), tenant.team); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "local_run_secrets", 0)
}

func TestDurableLocalSecretsAndObjectKeyRotateTogether(t *testing.T) {
	s := durableSecretStore(t, filepath.Join(t.TempDir(), "state.db"), durableSecretCipher(t))
	tenant, award := localSecretFixture(t, "rotation-private-value", s)
	key, err := s.AsOperator().LocalObjectSigningKey(t.Context(), s.localSecretsCipher)
	if err != nil {
		t.Fatal(err)
	}
	nextKey := bytes.Repeat([]byte{43}, secrets.KeySize)
	rotating, err := secrets.NewCipherWithPrevious(nextKey, bytes.Repeat([]byte{42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, verify := range []bool{true, false} {
		err = s.inTx(t.Context(), func(tx *storeTx) error {
			r := &sealedRotationTx{tx: tx, cipher: rotating, verify: verify}
			if _, _, err := r.runEnvironments(t.Context(), tenant.team, "", true); err != nil {
				return err
			}
			if err := r.settingKey(t.Context(), metaKeyLocalObjects); err != nil {
				return err
			}
			if r.out.Secrets != 1 || r.out.Settings != 1 || r.out.PreviousKeyOnly != 2 {
				t.Fatal("rotation missed local envelopes")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	next, err := secrets.NewCipher(nextKey)
	if err != nil {
		t.Fatal(err)
	}
	s.localSecretsCipher = next
	row, err := tenant.ReadSecret(t.Context(), award)
	if err != nil || row == nil || row.Value != "rotation-private-value" {
		t.Fatal("rotated secret does not open with current key alone")
	}
	reopened, err := s.AsOperator().LocalObjectSigningKey(t.Context(), next)
	if err != nil || !bytes.Equal(key, reopened) {
		t.Fatal("rotation changed signing key")
	}
}

func TestLocalObjectSigningKeyBlamesTheKeyOnlyForKeyMisses(t *testing.T) {
	s := durableSecretStore(t, filepath.Join(t.TempDir(), "state.db"), durableSecretCipher(t))
	if _, err := s.AsOperator().LocalObjectSigningKey(t.Context(), s.localSecretsCipher); err != nil {
		t.Fatal(err)
	}
	foreign, err := secrets.NewCipher(bytes.Repeat([]byte{44}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic *apierr.Error
	if _, err := s.AsOperator().LocalObjectSigningKey(t.Context(), foreign); !errors.As(err, &diagnostic) || diagnostic.Code != apierr.BadRequest {
		t.Fatalf("a signing key sealed under another key lost its key diagnostic: %v", err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE settings SET value=? WHERE key=?`, []byte(secrets.BoundPrefix+"!!!"), metaKeyLocalObjects); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().LocalObjectSigningKey(t.Context(), s.localSecretsCipher); err == nil || errors.As(err, &diagnostic) {
		t.Fatalf("a corrupt signing key was blamed on the secrets key: %v", err)
	}
}
