package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestWarmCapacityObservationThroughLauncherHTTP(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "warm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	raw := "swk_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	digest := sha256.Sum256([]byte(raw))
	root := capability.Capability{Prefix: "warm-launcher", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, ReservedPools: []string{}, IssuedAt: time.Unix(0, 1)}
	record := store.CapabilityRecord{Version: 1, Value: root}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.Exec(t.Context(), st, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,?,?,'service','launcher','controller',?,'delegate',1,?,'none',?,?,1,'fixture')`, hex.EncodeToString(digest[:]), root.Prefix, nil, []byte(`["claims.launch"]`), encoded, []byte(`[]`), []byte(`null`)); err != nil {
		t.Fatal(err)
	}
	engine := controller.NewEngineController(st, controller.EngineController{})
	handler := controller.New(st, nil).WithEngineController(engine).Handler()
	base, _, boundary, _ := fixture()
	kube := fake.NewClientset()
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CloudClassLabel: "2", PoolNamespaceLabel: base.Config.Namespace}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	for _, kind := range []string{"ready", "used", "bound", "unready", "other-deployment", "reserved", "reserved-taint", "unknown-class", "used-team", "quarantined"} {
		n := node.DeepCopy()
		n.Name = kind
		switch kind {
		case "used":
			n.Labels[usedRunLabel] = "run-old"
		case "bound":
			n.Labels[TeamNodeLabel] = "team-old"
		case "unready":
			n.Status.Conditions[0].Status = corev1.ConditionFalse
		case "other-deployment":
			n.Labels[PoolNamespaceLabel] = "other"
		case "reserved-taint":
			n.Spec.Taints = []corev1.Taint{{Key: reservationTaint, Value: "held", Effect: corev1.TaintEffectNoSchedule}}
		case "unknown-class":
			n.Labels[CloudClassLabel] = "sparkwing-2cpu"
		case "used-team":
			n.Labels[usedTeamLabel] = "team-old"
		case "reserved":
			n.Annotations = map[string]string{reservationAnnotation: "held"}
		case "quarantined":
			n.Annotations = map[string]string{quarantineAnnotation: "unsafe"}
		}
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), n, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	l, err := NewLauncher(t.Context(), RuntimeConfig{Kube: kube, Boundary: boundary, Jobs: base.Config, Demand: controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitNode}}, Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: &http.Client{Transport: poolControllerTransport{handler}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.reportWarmPools(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := st.AsOperator().WarmCapacity(t.Context())
	if err != nil || len(state.Classes) != 6 || state.Classes[0].Warm != 1 || state.Classes[1].Warm != 0 {
		t.Fatalf("state=%+v error=%v", state, err)
	}
	if err := kube.CoreV1().Nodes().Delete(t.Context(), "ready", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.reportWarmPools(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err = st.AsOperator().WarmCapacity(t.Context())
	if err != nil || state.Classes[0].Warm != 0 {
		t.Fatalf("stale sample=%+v %v", state, err)
	}
}
