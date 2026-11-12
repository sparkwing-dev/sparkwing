package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestNativeKeeperDiagnosticsUseExistingAttemptLogBinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, _ := nativeConfigFixture(t)
		records := &nativeLogRecords{objects: map[string][]byte{}}
		cfg.Logs = func(_ context.Context, p capability.Principal, s blob.LogSubject) (logseg.Uploader, error) {
			if p.Authority != cfg.Principal.Authority || s.RunID != "run" || s.Team != cfg.Principal.Capability.Team {
				t.Errorf("diagnostic changed authority: %+v", s)
			}
			records.view.Subject = s
			return records, nil
		}
		services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return &processListener{closed: make(chan struct{})}, nil })
		if err != nil {
			t.Fatal(err)
		}
		_, stop := keepNode(t.Context(), services.Config.Lease, diagnosticNodeRenewal{}, runspec.NodeRef{RunID: "run", NodeID: "job"}, runstate.Actor{NodeFence: runspec.NodeFence{Generation: 2}})
		synctest.Wait()
		if len(records.objects) != 0 {
			t.Fatal("pre-attempt diagnostics bypassed binding")
		}
		if _, err := services.Config.Executor.BindStorage(t.Context(), NodeInput{NodeID: "job", Generation: 2, Attempt: 3}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		stop()
		synctest.Wait()
		if err := services.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		var body bytes.Buffer
		reader := logseg.CommittedReader{Records: records, Objects: records}
		if _, err := reader.Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.String(), `"event":"lease_keeper"`) || !strings.Contains(body.String(), `"role":"NODE"`) {
			t.Fatalf("diagnostics missing from authorized logs: %s", body.String())
		}
	})
}

func TestKeeperDiagnosticLogHourlyUploadVolume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := &nativeLogRecords{objects: map[string][]byte{}}
		subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 1}}
		records.view.Subject = subject
		sink, err := logseg.NewSink(t.Context(), records, logseg.Options{Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		diagnostics := lease.NewDiagnostics(t.Context(), func(_ context.Context, e lease.Event) error {
			raw, err := json.Marshal(struct {
				TS              string      `json:"ts"`
				Level           string      `json:"level"`
				Event           string      `json:"event"`
				Attrs           lease.Event `json:"attrs"`
				HostLeaseKeeper bool        `json:"host_lease_keeper"`
			}{e.Wall.Format(time.RFC3339Nano), "debug", "lease_keeper", e, true})
			if err != nil {
				return err
			}
			return sink.AppendDiagnostic(append(raw, '\n'))
		})
		defer diagnostics.Close()
		keeper := lease.Keeper{Diagnostics: diagnostics, Identity: lease.Identity{Role: "OWNER", Run: "run"}, Lease: 3 * time.Minute, Interval: time.Minute, Renew: func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }}
		_, stop := keeper.Run(t.Context())
		synctest.Wait()
		time.Sleep(time.Hour - time.Nanosecond)
		stop()
		synctest.Wait()
		if _, err := sink.Close(t.Context(), logseg.End{}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(records.objects) != 1 {
			t.Fatalf("diagnostic-only stream forced %d uploads before seal", len(records.objects))
		}
		bytes := 0
		for _, raw := range records.objects {
			bytes += len(raw)
		}
		t.Logf("actual log sink: %d segment uploads plus final manifest; %d compressed bytes per keeper-hour", len(records.objects), bytes)
	})
}

type diagnosticNodeRenewal struct{}

func (diagnosticNodeRenewal) RenewNodeClaim(context.Context, runspec.NodeRef, runstate.Actor) (runstate.LeaseBeat, error) {
	return runstate.LeaseBeat{}, nil
}

func (diagnosticNodeRenewal) ReadNode(context.Context, runspec.NodeRef) (runstate.NodeRow, error) {
	return runstate.NodeRow{}, nil
}

func TestHostKeeperInstallationsKeepOwnerAndNodeSeparate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var events []lease.Event
		diagnostics := lease.NewDiagnostics(t.Context(), func(_ context.Context, e lease.Event) error { events = append(events, e); return nil })
		defer diagnostics.Close()
		template := lease.Keeper{Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics}
		owner := OwnerHost{Lease: template, Renew: func(context.Context, runstate.Actor, bool) (runstate.LeaseBeat, error) {
			return runstate.LeaseBeat{}, nil
		}}
		actor := runstate.Actor{Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Generation: 7}}}
		ctx, stopOwner := owner.keepOwner(t.Context(), actor, func() bool { return false }, func() bool { return false })
		_, stopNode := keepNode(ctx, template, diagnosticNodeRenewal{}, runspec.NodeRef{RunID: "run", NodeID: "job"}, runstate.Actor{NodeFence: runspec.NodeFence{Generation: 2}})
		synctest.Wait()
		stopNode()
		stopOwner()
		synctest.Wait()
		roles := map[string]string{}
		for _, e := range events {
			roles[e.Identity.Role] = e.Instance
			if e.Identity.Run != "run" {
				t.Fatalf("wrong run: %+v", e)
			}
			if e.Identity.Role == "OWNER" && (e.Identity.Generation != 7 || e.Identity.Unit != "trigger") {
				t.Fatalf("wrong owner fence: %+v", e)
			}
			if e.Identity.Role == "NODE" && (e.Identity.Generation != 2 || e.Identity.Node != "job") {
				t.Fatalf("wrong node fence: %+v", e)
			}
		}
		if len(roles) != 2 || roles["OWNER"] == roles["NODE"] {
			t.Fatalf("installations conflated: %v", roles)
		}
	})
}

func TestDiagnosticBytesRideRealOutputCut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := &nativeLogRecords{objects: map[string][]byte{}}
		subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 1}}
		records.view.Subject = subject
		sink, err := logseg.NewSink(t.Context(), records, logseg.Options{Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.AppendDiagnostic([]byte("diagnostic\n")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if len(records.objects) != 0 {
			t.Fatal("diagnostic armed an upload timer")
		}
		if err := sink.Append([]byte("real output\n")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if len(records.objects) != 1 {
			t.Fatalf("real output did not flush diagnostics: %d", len(records.objects))
		}
		if _, err := sink.Close(t.Context(), logseg.End{}); err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		if _, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: subject}, &body); err != nil {
			t.Fatal(err)
		}
		if body.String() != "diagnostic\nreal output\n" {
			t.Fatalf("cut lost order: %q", body.String())
		}
	})
}

func TestKeeperDiagnosticStampSurvivesMasking(t *testing.T) {
	records := &nativeLogRecords{objects: map[string][]byte{}}
	masker := secrets.NewMasker()
	masker.Register("lease_keeper")
	logs := &localLogs{run: "run", masker: masker, principal: capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitTrigger, Binding: capability.Binding{RunID: "run", Generation: 1}}}, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		records.view.Subject = subject
		return records, nil
	}}
	if err := logs.write(t.Context(), "run", "", nodewire.FrameLog{Level: "debug", Event: "lease_keeper", Attrs: json.RawMessage(`{"sequence":1}`)}); err != nil {
		t.Fatal(err)
	}
	<-logs.finish(t.Context(), "")
	var body bytes.Buffer
	if _, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "***" || record["host_lease_keeper"] != true {
		t.Fatalf("masking lost diagnostic identity: %s", body.String())
	}
}
