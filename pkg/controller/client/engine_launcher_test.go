package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func FuzzLauncherSyncPreservesAwardAndJobInstance(f *testing.F) {
	f.Add(int64(1), "job-instance", uint8(0))
	f.Add(int64(7), "replacement-instance", uint8(1))
	f.Add(int64(9), "", uint8(2))
	f.Fuzz(func(t *testing.T, generation int64, uid string, kind uint8) {
		kinds := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}
		input := controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{{
			Award: capability.LeaseRef{Kind: kinds[int(kind)%len(kinds)], ID: "stable-unit", Holder: "launcher", Generation: generation},
			Job:   controllerwire.LauncherJobRef{Namespace: "ci", Name: "job", UID: uid}, NodeUID: "node-instance",
		}}}
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			var body controllerwire.LauncherSyncRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			var normalized controllerwire.LauncherSyncRequest
			if err := json.Unmarshal(expected, &normalized); err != nil || !reflect.DeepEqual(body, normalized) {
				t.Fatalf("observation lost its fence or instance: %+v %v", body, err)
			}
			response, err := json.Marshal(controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Remove: []controllerwire.LauncherJobRef{input.Observed[0].Job}})
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(200, string(response)), nil
		})
		result, err := c.SyncLauncher(t.Context(), input)
		valid := generation > 0 && uid != ""
		if valid && (err != nil || calls != 1 || len(result.Remove) != 1) || !valid && (err == nil || calls != 0) {
			t.Fatalf("sync result: %+v %v calls=%d", result, err, calls)
		}
	})
}

func TestLauncherSyncRejectsNameOnlyCleanup(t *testing.T) {
	for _, body := range []string{
		`{"awards":[],"remove":["job"]}`,
		`{"awards":[],"remove":[{"namespace":"ci","name":"job"}]}`,
		`{"awards":[],"remove":[{"namespace":"ci","name":"job","uid":""}]}`,
		`{"awards":[],"remove":[{"namespace":"other/ci","name":"job","uid":"instance"}]}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		result, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{})
		if err == nil || len(result.Remove) != 0 {
			t.Fatalf("unfenced cleanup accepted: %+v %v", result, err)
		}
	}
}

func TestLauncherSyncRejectsIncompleteObservation(t *testing.T) {
	valid := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: "unit", Holder: "launcher", Generation: 2}, Job: controllerwire.LauncherJobRef{Namespace: "ci", Name: "job", UID: "instance"}, NodeUID: "node-instance"}
	for _, change := range []func(*controllerwire.LauncherObservation){
		func(o *controllerwire.LauncherObservation) { o.Award.Kind = capability.UnitTrigger },
		func(o *controllerwire.LauncherObservation) { o.Award.ID = "" },
		func(o *controllerwire.LauncherObservation) { o.Award.Holder = "" },
		func(o *controllerwire.LauncherObservation) { o.Award.Generation = 0 },
		func(o *controllerwire.LauncherObservation) { o.Job.UID = "" },
		func(o *controllerwire.LauncherObservation) { o.NodeUID = "" },
	} {
		observed := valid
		change(&observed)
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("incomplete observation reached transport")
			return nil, nil
		})
		_, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{observed}})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.BadRequest {
			t.Fatalf("invalid observation: %v", err)
		}
	}
}

func FuzzLauncherSyncNeverReturnsBearer(f *testing.F) {
	f.Add("private-bootstrap", false)
	f.Add("private-bootstrap", true)
	f.Fuzz(func(t *testing.T, secret string, malformed bool) {
		encoded, err := json.Marshal(controllerwire.ClaimedUnit{Capability: "private:" + secret})
		if err != nil {
			t.Fatal(err)
		}
		var award map[string]any
		if err := json.Unmarshal(encoded, &award); err != nil {
			t.Fatal(err)
		}
		response := map[string]any{
			"awards": []any{award},
			"remove": []any{},
		}
		if malformed {
			award["owner_deadline"] = "malformed timestamp"
		}
		body, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
		result, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{})
		if err == nil || len(result.Awards) != 0 || len(result.Remove) != 0 {
			t.Fatal("sync exposed a partial response containing private authority")
		}
	})
}
