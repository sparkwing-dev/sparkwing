package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
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
	st, err := teststore.Open(t, filepath.Join(t.TempDir(), "warm.db"))
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
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{CloudClassLabel: "2", PoolNamespaceLabel: base.Config.Namespace, NodeClassLabel: base.Config.NodeClassName}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	for _, kind := range []string{"ready", "used", "bound", "unready", "other-deployment", "reserved", "reserved-taint", "unknown-class", "used-team", "quarantined", "other-node-class"} {
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
		case "other-node-class":
			n.Labels[NodeClassLabel] = "sparkwing-staging-boot"
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

type warmSampleControl struct {
	*controlFake
	warm map[string]int64
}

func (c *warmSampleControl) ReportWarmPools(_ context.Context, warm map[string]int64) error {
	c.warm = warm
	return nil
}

// Staging's boot and jobs NodePools shared the class labels and taint, so
// placeholders held boot-class nodes the launcher refuses. Those nodes must not
// count as warm, and the misconfiguration is reported without flooding the log.
func TestWarmSampleExcludesAndReportsNodesOfAnotherNodeClass(t *testing.T) {
	l, kube, _, base := fixture()
	control := &warmSampleControl{controlFake: base}
	var logs strings.Builder
	l.Control, l.Logger = control, slog.New(slog.NewTextHandler(&logs, nil))
	for name, class := range map[string]string{"jobs": l.Config.NodeClassName, "boot": "sparkwing-staging-boot"} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{CloudClassLabel: "2", PoolNamespaceLabel: l.Config.Namespace, NodeClassLabel: class, "karpenter.sh/nodepool": "pool-" + name}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := l.reportWarmPools(t.Context()); err != nil {
			t.Fatal(err)
		}
		if control.warm["sparkwing-2cpu"] != 1 {
			t.Fatalf("warm sample counted another node class: %v", control.warm)
		}
	}
	warning := logs.String()
	if strings.Count(warning, "level=WARN") != 1 {
		t.Fatalf("want one rate-limited WARN, got %q", warning)
	}
	for _, want := range []string{"boot (NodePool pool-boot, EC2NodeClass sparkwing-staging-boot)", "node_class=" + l.Config.NodeClassName} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning %q omits %q", warning, want)
		}
	}
	if strings.Contains(warning, "pool-jobs") {
		t.Fatalf("warning names an accepted node: %q", warning)
	}
}
