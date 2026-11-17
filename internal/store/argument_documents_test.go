package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestArgumentDocumentsPreservePlaintextBytesAndBindings(t *testing.T) {
	s := bindingStore(t)
	raw := []byte(`{ "jobs":[{"id":"child","arguments":[{"default":"binding-canary-91f2"}]}],"unknown":9007199254740993,"escaped":"\u0041" }`)
	for _, scope := range []string{planArgumentsScope, nodeArgumentsScope("job"), expansionBodyArgumentsScope("job"), scheduleArgumentsScope("args"), scheduleArgumentsScope("override_args"), scheduleArgumentsScope("override_base"), scheduleArgumentsScope("consent_json"), fireArgumentsScope("schedule")} {
		t.Run(scope, func(t *testing.T) {
			stored, err := encodeArgumentBody(s.runEnvCipher, "a", "id", scope, raw)
			if err != nil || bytes.Contains(stored, []byte(bindingCanary)) {
				t.Fatalf("document was not sealed: %v", err)
			}
			back, err := decodeArgumentBody(s, "a", "id", scope, stored)
			if err != nil || !bytes.Equal(back, raw) {
				t.Fatalf("plaintext bytes changed: %v", err)
			}
			legacy, err := decodeArgumentBody(s, "a", "id", scope, raw)
			if err != nil || !bytes.Equal(legacy, raw) {
				t.Fatalf("legacy bytes changed: %v", err)
			}
			for _, binding := range []struct {
				team      Team
				id, scope string
			}{{"b", "id", scope}, {"a", "other", scope}, {"a", "id", "other:" + scope}} {
				opened, err := decodeArgumentBody(s, binding.team, binding.id, binding.scope, stored)
				var typed *apierr.Error
				if !errors.As(err, &typed) || typed.Code != hosterr.IntegrityFailure || len(opened) > 0 || bytes.Contains([]byte(err.Error()), []byte(bindingCanary)) {
					t.Fatalf("wrong binding opened or leaked: %v", err)
				}
			}
		})
	}
}

func TestScheduleConsentArgumentsAreSealedBeforeAdmission(t *testing.T) {
	tenant, a, body, now := consentedScheduleFixture(t)
	tenant.s.runEnvCipher = bindingStore(t).runEnvCipher
	body.Schedules[0].Args = map[string]string{"token": bindingCanary}
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := tenant.s.queryRow(t.Context(), `SELECT consent_json FROM cron_schedules WHERE team=? AND id=?`, tenant.team, ids[0]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(bindingCanary)) {
		t.Fatal("consent contains plaintext argument")
	}
	row, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	*now = *row.NextDueAt
	first, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
	if err != nil || first.Request.Request.RequestedArgs["token"] != bindingCanary {
		t.Fatalf("consent admission: %v", err)
	}
	second, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
	if err != nil || !second.Replayed || second.RunID != first.RunID {
		t.Fatalf("consent replay: %v", err)
	}
}

func TestArgumentDocumentRotationCountsWithoutChangingVerification(t *testing.T) {
	old, err := secrets.NewCipher(bytes.Repeat([]byte{1}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	current, err := secrets.NewCipherWithPrevious(bytes.Repeat([]byte{2}, secrets.KeySize), bytes.Repeat([]byte{1}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	next, err := secrets.NewCipher(bytes.Repeat([]byte{2}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"arguments":[{"default":"binding-canary-91f2"}]}`)
	stored, err := encodeArgumentBody(old, "a", "run", "plan", raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		input := stored
		if legacy {
			input = raw
		}
		verify := &sealedRotationTx{cipher: current, verify: true}
		value, err := verify.argumentBody("a", "run", "plan", input, true)
		if err != nil || value != nil {
			t.Fatalf("verification wrote a document: %v", err)
		}
		if legacy && verify.out.PlaintextRunArguments != 1 || !legacy && (verify.out.PreviousKeyOnly != 1 || verify.out.RunArguments != 1) {
			t.Fatalf("verification counts: %+v", verify.out)
		}
		rotate := &sealedRotationTx{cipher: current}
		value, err = rotate.argumentBody("a", "run", "plan", input, true)
		if err != nil || !json.Valid(value) || bytes.Contains(value, []byte(bindingCanary)) {
			t.Fatalf("rotation: %v", err)
		}
		back, err := decodeArgumentBody(&Store{runEnvCipher: next}, "a", "run", "plan", value)
		if err != nil || !bytes.Equal(back, raw) {
			t.Fatalf("current key round trip: %v", err)
		}
	}
}
