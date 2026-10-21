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

func FuzzLauncherSyncPreservesAwardFence(f *testing.F) {
	f.Add(int64(1), uint8(0), false)
	f.Add(int64(7), uint8(1), true)
	f.Add(int64(0), uint8(2), false)
	f.Fuzz(func(t *testing.T, generation int64, kind uint8, reservation bool) {
		kinds := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}
		observed := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: kinds[kind%3], ID: "unit", Holder: "launcher", Generation: generation}, Reservation: reservation}
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			var body controllerwire.LauncherSyncRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || !reflect.DeepEqual(body.Observed, []controllerwire.LauncherObservation{observed}) {
				t.Fatalf("award changed: %+v %v", body, err)
			}
			response, err := json.Marshal(controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Retired: []capability.LeaseRef{observed.Award}})
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(200, string(response)), nil
		})
		result, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{observed}})
		if generation > 0 {
			if err != nil || calls != 1 || len(result.Retired) != 1 || result.Retired[0] != observed.Award {
				t.Fatalf("sync: %+v %v", result, err)
			}
		} else if err == nil || calls != 0 {
			t.Fatal("unfenced observation reached transport")
		}
	})
}

func TestLauncherSyncRejectsIncompleteObservation(t *testing.T) {
	valid := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: "unit", Holder: "launcher", Generation: 2}}
	for _, change := range []func(*controllerwire.LauncherObservation){
		func(o *controllerwire.LauncherObservation) { o.Award.Kind = capability.UnitTrigger },
		func(o *controllerwire.LauncherObservation) { o.Award.ID = "" },
		func(o *controllerwire.LauncherObservation) { o.Award.Holder = "" },
		func(o *controllerwire.LauncherObservation) { o.Award.Generation = 0 },
		func(o *controllerwire.LauncherObservation) { o.Reservation = true; o.Rejected = true },
	} {
		observed := valid
		change(&observed)
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid observation reached transport")
			return nil, nil
		})
		_, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{observed}})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.BadRequest {
			t.Fatalf("invalid observation: %v", err)
		}
	}
}

func TestLauncherSyncRefusesIncompleteRetirement(t *testing.T) {
	for _, body := range []string{`{"retired":["job"]}`, `{"retired":[{"id":"unit"}]}`, `{"retired":[{"kind":"trigger","id":"unit","holder":"launcher","generation":1}]}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if result, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{}); err == nil || len(result.Retired) != 0 {
			t.Fatalf("unfenced retirement: %+v %v", result, err)
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
			"awards":  []any{award},
			"retired": []any{},
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
		if err == nil || len(result.Awards) != 0 || len(result.Retired) != 0 {
			t.Fatal("sync exposed a partial response containing private authority")
		}
	})
}
