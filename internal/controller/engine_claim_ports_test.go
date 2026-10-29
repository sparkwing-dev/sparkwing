package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestEngineClaimHTTPRemovedSelectorsNeverAwardProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.6s measured in the short class; the full suite runs it")
	}
	property := func(selection uint8, value string) bool {
		e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
		fields := []string{"pipelines", "sources", "allow_repos", "repos", "holder_id", "lease_secs", "capacity", "available_cores", "available_memory_bytes"}
		field := fields[int(selection)%len(fields)]
		body, err := json.Marshal(map[string]any{"runner_version": "v1", "kinds": []string{"node"}, field: value})
		if err != nil {
			t.Fatal(err)
		}
		w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", string(body), http.StatusBadRequest)
		var count int
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND subject_id='root'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 || strings.Contains(w.Body.String(), `"capability"`) {
			return false
		}
		w = engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, http.StatusOK)
		var got controllerwire.ClaimResponse
		return json.Unmarshal(w.Body.Bytes(), &got) == nil && got.Claim != nil && got.Claim.RunID == "run" && got.Claim.JobID == "job"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 28}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineClaimHTTPRepositorySelectionUsesEnrollment(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.1s measured in the short class; the full suite runs it")
	}
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		for _, named := range []bool{false, true} {
			for _, scope := range []string{"unrestricted", "empty", "other", "accepted"} {
				t.Run(string(kind)+"/"+map[bool]string{false: "queue", true: "named"}[named]+"/"+scope, func(t *testing.T) {
					e, _ := engineRuntimeStore(t)
					if kind == capability.UnitNode {
						engineRuntimeClaim(t, e, capability.UnitTrigger)
						engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
						engineAcceptedFixturePlan(t, e, "a", "run")
					}
					engineSQL(t, e.Store, `UPDATE runs SET repo_display='https://github.com/acme/repo' WHERE id='run'`)
					var accept match.RepoFilter
					if scope != "unrestricted" {
						patterns := []string{}
						if scope != "empty" {
							patterns = []string{"github.com/" + map[string]string{"other": "other", "accepted": "acme"}[scope] + "/*"}
						}
						var err error
						accept, err = match.ParseAccept(patterns)
						if err != nil {
							t.Fatal(err)
						}
					}
					e.claimProfiles = engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, error) {
						return match.Profile{Accept: accept}, nil
					})
					e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
					raw := engineHTTPRoot(t, e, "a", "root", []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
					body := controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{kind}}
					if named {
						body.RunID = "run"
						if kind == capability.UnitNode {
							body.JobID = "job"
						}
					}
					encoded, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					allowed := scope == "unrestricted" || scope == "accepted"
					status := http.StatusOK
					if named && !allowed {
						status = http.StatusForbidden
					}
					h := e.Server.WithEngineController(e.EngineController).Handler()
					w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", string(encoded), status)
					var response controllerwire.ClaimResponse
					if status == http.StatusOK && (json.Unmarshal(w.Body.Bytes(), &response) != nil || (response.Claim != nil) != allowed) {
						t.Fatalf("scope=%s: %s", scope, w.Body)
					}
					var awards int
					if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND subject_id='root'`).Scan(&awards); err != nil || awards != map[bool]int{false: 0, true: 1}[allowed] {
						t.Fatalf("awards=%d: %v", awards, err)
					}
				})
			}
		}
	}
}

func TestEngineClaimHTTPTriggerQueueKeepsOrder(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `UPDATE runs SET created_at=1 WHERE id='run'`)
	for i, id := range []string{"second", "third"} {
		engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,?,number+?,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,?,log_retention_class,log_retention_days FROM runs WHERE id='run'`, id, i+1, i+2)
		engineSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,?,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`, id)
	}
	e.claimProfiles = engineProfileFake{}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	raw := engineHTTPRoot(t, e, "a", "root", []capability.Grant{capability.TriggersClaim})
	h := e.Server.WithEngineController(e.EngineController).Handler()
	for _, id := range []string{"run", "second", "third", ""} {
		w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["trigger"]}`, http.StatusOK)
		var response controllerwire.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			if response.Claim != nil {
				t.Fatal("empty queue returned an award")
			}
		} else if response.Claim == nil || response.Claim.RunID != id || response.Claim.Capability == "" {
			t.Fatalf("ordered claim for %s: %s", id, w.Body)
		}
	}
}

func TestEngineTriggerClaimHTTPConsumesContractReplay(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	e.claimProfiles = engineProfileFake{}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	raw := engineHTTPRoot(t, e, "a", "root", []capability.Grant{capability.TriggersClaim})
	h := e.Server.WithEngineController(e.EngineController).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/claim", `{"runner_version":"v1","kinds":["trigger"]}`, http.StatusOK)
	var claimed controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &claimed); err != nil || claimed.Claim == nil {
		t.Fatalf("claim: %s %v", w.Body, err)
	}
	claim := claimed.Claim.Capability
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/heartbeat", `{}`, http.StatusForbidden)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/foreign/heartbeat", `{}`, http.StatusNotFound)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/heartbeat", `{"lease_secs":86400}`, http.StatusBadRequest)
	w = engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/heartbeat", `{"executing":true}`, http.StatusOK)
	var renewal controllerwire.RenewalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &renewal); err != nil || !renewal.ExpiresAt.After(time.Now()) || renewal.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatalf("renewal: %s %v", w.Body, err)
	}
	var first controllerwire.Applied
	for i := range 2 {
		w = engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/finish", `{}`, http.StatusOK)
		var result controllerwire.Applied
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Replayed != (i == 1) || i == 1 && result.StateSeq != first.StateSeq {
			t.Fatalf("finish %d: %s %v", i, w.Body, err)
		}
		first = result
		if i == 0 {
			digest := sha256.Sum256([]byte(claim))
			snapshot, err := e.Store.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: digest, Audience: capability.Controller}, capability.RequestEvidence{})
			if err != nil {
				t.Fatal(err)
			}
			bodyDigest := sha256.Sum256([]byte(`{}`))
			snapshot.Binding.Result = &capability.RecordedResult{
				Actor:  snapshot.Authorities[0].Principal.Authority,
				Method: "POST", Pattern: "/api/v1/runs/{run}/finish", RunID: "run", Generation: claimed.Claim.Binding.Generation, Digest: hex.EncodeToString(bodyDigest[:]),
			}
			server := e.Server.WithEngineController(e.EngineController)
			authorization := server.authorization
			authorization.Authenticator = capability.NewAuthenticator(&reportingFacts{snapshot: snapshot, digest: digest})
			h = server.WithAuthorization(authorization).Handler()
		}
	}
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/finish", `{"reason":"different"}`, http.StatusUnauthorized)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/heartbeat", `{}`, http.StatusUnauthorized)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || string(run.State) != "failed" || run.FinishedAt == nil {
		t.Fatalf("finished run: %+v %v", run, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var finished int
	for _, event := range events {
		if event.Kind == "run_failed" {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("finish events: %+v", events)
	}
}

func TestEngineNamedClaimHTTPRequiresReadyMatchingWork(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.1s measured in the short class; the full suite runs it")
	}
	for _, state := range []string{"ready", "held", "missing", "wrong grant", "unready", "finished", "missing label", "matching label"} {
		t.Run(state, func(t *testing.T) {
			e, h, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
			body := `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job"}`
			status := http.StatusOK
			switch state {
			case "held":
				engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", body, http.StatusOK)
				status = http.StatusConflict
			case "missing":
				body = strings.ReplaceAll(body, `"job"`, `"missing"`)
				status = http.StatusForbidden
			case "wrong grant":
				raw = engineHTTPRoot(t, e, "a", "reader", []capability.Grant{capability.RunsRead})
				status = http.StatusForbidden
			case "unready":
				engineSQL(t, e.Store, `UPDATE nodes SET ready_at=NULL WHERE node_id='job'`)
				status = http.StatusConflict
			case "finished":
				engineSQL(t, e.Store, `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`)
				status = http.StatusConflict
			case "missing label", "matching label":
				engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, []byte(`{"requires":{"set":true,"value":["gpu"]}}`))
				status = http.StatusForbidden
				if state == "matching label" {
					body = strings.TrimSuffix(body, "}") + `,"labels":["gpu"]}`
					status = http.StatusOK
				}
			}
			w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", body, status)
			var count int
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM capabilities WHERE issuance='award' AND subject_id='root'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if status == http.StatusOK || state == "held" {
				want = 1
			}
			if count != want || status != http.StatusOK && strings.Contains(w.Body.String(), `"capability"`) {
				t.Fatalf("refused claim changed award: %d %s", count, w.Body)
			}
		})
	}
}

func TestNamedClaimNumericSelectorConvergesAcrossTransports(t *testing.T) {
	for _, transport := range []string{"http", "embedded"} {
		t.Run(transport, func(t *testing.T) {
			e, _ := engineRuntimeStore(t)
			e.claimProfiles = engineProfileFake{}
			e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
			raw := engineHTTPRoot(t, e, "a", "root", []capability.Grant{capability.TriggersClaim})
			var number int64
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT number FROM runs WHERE team='a' AND id='run'`).Scan(&number); err != nil {
				t.Fatal(err)
			}
			selector := strconv.FormatInt(number, 10)
			body := controllerwire.ClaimRequest{RunnerVersion: "v1", RunID: selector, Kinds: []capability.UnitKind{capability.UnitTrigger}}
			var response controllerwire.ClaimResponse
			if transport == "http" {
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				w := engineClaimHTTP(t, e.Server.WithEngineController(e.EngineController).Handler(), raw, "POST", "/api/v1/runs/"+selector+"/claim", string(encoded), http.StatusOK)
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
			} else {
				router, err := e.ClaimPolicyRouter()
				if err != nil {
					t.Fatal(err)
				}
				ctx, authorized, err := router.Authorize(t.Context(), "POST", "/api/v1/runs/{run}/claim", capability.Presentation{Raw: raw, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: capability.Target{RunID: selector, ClaimKinds: body.Kinds}}})
				if err != nil {
					t.Fatal(err)
				}
				response, err = e.Claim(ctx, authorized, body)
				if err != nil {
					t.Fatal(err)
				}
			}
			if response.Claim == nil || response.Claim.RunID != "run" {
				t.Fatalf("claim=%+v", response.Claim)
			}
		})
	}
}
