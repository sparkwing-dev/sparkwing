package controller

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func TestLocalDoctorRecordsOnlyTheLocalTenant(t *testing.T) {
	for _, scope := range []string{"pruned", "local", "foreign"} {
		for _, command := range []string{"true", "false"} {
			for _, dry := range []bool{true, false} {
				t.Run(scope+"/"+command+"/"+map[bool]string{true: "dry", false: "sweep"}[dry], func(t *testing.T) {
					st := newStoreForAuth(t)
					team := store.DefaultTeam
					if scope == "foreign" {
						team = "foreign"
						teststore.SeedTeam(t, st, team)
					}
					if scope != "pruned" {
						if err := teststore.SeedRun(st, t.Context(), team, store.Run{RunSummary: runstate.RunSummary{ID: "selected", Pipeline: "ci", Status: "pending"}}); err != nil {
							t.Fatal(err)
						}
					}
					directory := filepath.Join(t.TempDir(), "sessions")
					ledger := sessionledger.Open(directory)
					owner := exec.Command("true")
					if err := owner.Run(); err != nil {
						t.Fatal(err)
					}
					if _, err := ledger.Record(sessionledger.Record{Run: "selected", Node: "build", OwnerPID: owner.Process.Pid, OwnerBirth: "gone", Handle: sessionledger.Handle{Kind: "command", ID: "cleanup", Argv: []string{command}}}); err != nil {
						t.Fatal(err)
					}
					server := New(st, nil).WithLocalSessions(directory)
					listener := &localScheduleListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
					ctx, cancel := context.WithCancel(t.Context())
					done := make(chan error, 1)
					go func() { done <- wingd.ServeAPI(ctx, listener, server.HTTP()) }()
					peer, _ := localControllerIdentity(t)
					transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
						local, remote := net.Pipe()
						listener.connections <- localScheduleConnection{Conn: remote, peer: peer}
						return local, nil
					}}
					t.Cleanup(func() {
						transport.CloseIdleConnections()
						cancel()
						if err := <-done; err != nil {
							t.Error(err)
						}
					})
					findings, err := client.SweepLocalSessions(t.Context(), client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: transport}}, dry)
					if (err != nil) != (!dry && command == "false") {
						t.Fatalf("cleanup failure=%t: %v", !dry && command == "false", err)
					}
					left, err := ledger.List()
					if err != nil || len(left) != map[bool]int{true: 1, false: 0}[dry] {
						t.Fatalf("sweep records %d: %v", len(left), err)
					}
					want := map[bool]string{true: "stray_step_session", false: "unrecorded_step_outcome"}[dry]
					if !dry && scope == "local" {
						wantFindings := 0
						if command == "false" {
							wantFindings = 1
						}
						if len(findings) != wantFindings {
							t.Fatalf("local findings: %+v", findings)
						}
					} else if len(findings) < 1 || findings[0].Code != want || findings[0].Unit != "selected" || findings[0].Message == "" {
						t.Fatalf("lost outcome: %+v", findings)
					}
					if scope != "pruned" {
						events, err := teststore.ForTeam(t, st, team).ListEventsAfter(t.Context(), "selected", 0, 10)
						want := 0
						if !dry && scope == "local" {
							want = 1
						}
						if err != nil || len(events) != want {
							t.Fatalf("%s events: %+v, %v; want %d", scope, events, err, want)
						}
					}
				})
			}
		}
	}
}

func FuzzLocalDoctorRejectsRequestIdentity(f *testing.F) {
	f.Add("local:123", "123", "forged")
	f.Fuzz(func(t *testing.T, subject, uid, bearer string) {
		r := httptest.NewRequest("POST", "/api/v1/local/doctor", nil)
		r.Header.Set("X-Local-UID", uid)
		r.Header.Set("Authorization", bearer)
		a := capability.Authorized{Principal: capability.Principal{Class: capability.CredentialPeer, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectLocal, ID: subject}}}, Target: capability.Target{Team: string(store.DefaultTeam)}}
		if err := new(Server).localDoctor(httptest.NewRecorder(), r, a); err == nil {
			t.Fatal("request fields authorized the local session sweep")
		}
	})
}
