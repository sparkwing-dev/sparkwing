package nodehost

import (
	"encoding/json"
	"errors"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestRecordedSDKProtocols(t *testing.T) {
	paths, err := filepath.Glob("../nodeproto/conformance/testdata/recordings/v*/*/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("recordings: %v, %v", paths, err)
	}
	languages := map[int]map[string]bool{}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var recording conformance.Recording
			if err := json.Unmarshal(raw, &recording); err != nil {
				t.Fatal(err)
			}
			if languages[recording.Protocol] == nil {
				languages[recording.Protocol] = map[string]bool{}
			}
			languages[recording.Protocol][recording.Language] = true
			s := &protocolSession{pending: map[string]protocolCall{}, cfg: ProtocolSessionConfig{Revoke: func() error { return nil }}}
			for _, exchange := range recording.Exchanges {
				request := exchange.Request
				if request.Op == nodewire.OpHello {
					if _, err := nodewire.ParseRequest(exchange.WireRequest); err != nil {
						t.Fatalf("published hello bootstrap: %v", err)
					}
				}
				expected := exchange.Expected
				if expected.ID != request.ID {
					t.Fatalf("missing reply for %s", request.ID)
				}
				if err := s.acceptProtocolRequest(request); err != nil {
					var failure *apierr.Error
					if expected.Error == nil || !errors.As(err, &failure) || failure.Code != expected.Error.Code {
						t.Fatalf("%s: %v, want %+v", request.Op, err, expected)
					}
					continue
				}
				wire, err := s.encodeProtocolRequest(request)
				if err != nil {
					t.Fatal(err)
				}
				var sent, wantWire any
				expectedWire := exchange.WireRequest
				if err := json.Unmarshal(expectedWire, &wantWire); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(wire, &sent); err != nil || !reflect.DeepEqual(sent, wantWire) {
					t.Fatalf("outgoing request = %s, %v", wire, err)
				}
				call := protocolCall{request: request, callerID: request.ID, reply: make(chan nodewire.Reply, 1)}
				s.pending[request.ID] = call
				for _, raw := range exchange.Frames {
					upgraded, err := s.upgradeProtocolFrame(raw)
					if err != nil {
						t.Fatal(err)
					}
					frame, err := nodewire.ParseFrame(upgraded)
					if err != nil {
						t.Fatal(err)
					}
					if frame.Reply != nil {
						if err := s.deliverProtocolReply(*frame.Reply); err != nil {
							t.Fatal(err)
						}
					}
				}
				select {
				case got := <-call.reply:
					assertRecordedOutcome(t, request, got, expected)
				default:
					t.Fatal("engine did not deliver recorded reply")
				}
			}
			if s.sdkProtocol != recording.Protocol || !s.ready {
				t.Fatalf("handshake recorded protocol %d, ready %v", s.sdkProtocol, s.ready)
			}
		})
	}
	for version := range protocolAdapters {
		for _, language := range []string{"go", "typescript"} {
			if !languages[version][language] {
				t.Errorf("protocol %d has no %s recording", version, language)
			}
		}
	}
}

func assertRecordedOutcome(t *testing.T, request nodewire.Request, got, expected nodewire.Reply) {
	t.Helper()
	var destination any
	switch request.Op {
	case nodewire.OpHello:
		destination = &nodewire.HelloResult{}
	case nodewire.OpDescribe:
		destination = &nodewire.DescribeDoc{}
	case nodewire.OpPlan:
		destination = &nodewire.PlanDoc{}
	case nodewire.OpRunStep:
		destination = &nodewire.RunResult{}
	case nodewire.OpEval:
		destination = &nodewire.EvalResult{}
	default:
		destination = &map[string]any{}
	}
	if err := got.Decode(destination); err != nil {
		var failure *apierr.Error
		if expected.Error == nil || !errors.As(err, &failure) || failure.Code != expected.Error.Code {
			t.Fatal(err)
		}
		return
	}
	if expected.Error != nil {
		t.Fatalf("lost recorded failure: %+v", expected.Error)
	}
	if request.Op == nodewire.OpPlan {
		if _, err := nodeproto.ValidatePlan(got.Result, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: recordedSecrets{}}); err != nil {
			t.Fatal(err)
		}
	}
	var want, actual any
	if err := json.Unmarshal(expected.Result, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Result, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("%s outcome = %s, want %s", request.Op, got.Result, expected.Result)
	}
}

func TestProtocolAdapterRejectsUnknownVersion(t *testing.T) {
	s := &protocolSession{pending: map[string]protocolCall{"hello": {request: nodewire.Request{Op: nodewire.OpHello}}}}
	_, err := s.upgradeProtocolFrame([]byte(`{"reply":"hello","ok":true,"result":{"protocol":999,"sdk":{"language":"go","version":"test"}}}`))
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolMismatch || s.sdkProtocol != 0 {
		t.Fatalf("unsupported hello = %v, protocol %d", err, s.sdkProtocol)
	}
}

func TestProtocolAdapterUpgradesBeforeValidation(t *testing.T) {
	s := &protocolSession{sdkProtocol: 1, adapter: protocolAdapter{frame: func([]byte) ([]byte, error) {
		return []byte(`{"reply":"old","ok":true,"result":{"outcome":"success","exit_code":0}}`), nil
	}}}
	line, err := s.upgradeProtocolFrame([]byte(`{"old_outcome":true}`))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := nodewire.ParseFrame(line)
	if err != nil {
		t.Fatal(err)
	}
	var result nodewire.RunResult
	if err := frame.Reply.Decode(&result); err != nil || result.Outcome != "success" {
		t.Fatalf("upgraded outcome = %+v, %v", result, err)
	}
}

type recordedSecrets struct{}

func (recordedSecrets) ContainsSecret(string) bool { return false }

func TestProtocolAdapterTranslatesOutgoingShape(t *testing.T) {
	s := &protocolSession{sdkProtocol: 1, adapter: protocolAdapter{request: func(raw []byte) ([]byte, error) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		fields["legacy_job"] = fields["job"]
		delete(fields, "job")
		return json.Marshal(fields)
	}}}
	raw, err := s.encodeProtocolRequest(nodewire.Request{Op: nodewire.OpRunStep, Job: "task"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["legacy_job"]) != `"task"` || fields["job"] != nil {
		t.Fatalf("outgoing shape = %s", raw)
	}
}
