package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestLauncherCapacityWaitDoesNotSealExecutionHost(t *testing.T) {
	server, root := storedCloudClaimantW8(t)
	_, cfg, _ := cloudFixture(launcher.CloudWork)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "dedicated", UID: "execution-node", Labels: map[string]string{launcher.TeamNodeLabel: launcher.TeamNodeValue("cloud", "default")}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	provisioner := cloudProvisionerW8(node, "default")
	kube := cloudKubeW8()
	host := func(want string) {
		t.Helper()
		var body []byte
		err := teststore.SQL(t, server.Store).QueryRowContext(t.Context(), `SELECT c.authority_json FROM capabilities c JOIN nodes n ON c.prefix=n.capability_prefix WHERE n.team='default' AND n.run_id='run' AND n.node_id='job'`).Scan(&body)
		if err != nil {
			t.Fatal(err)
		}
		var record store.CapabilityRecord
		if err := json.Unmarshal(body, &record); err != nil {
			t.Fatal(err)
		}
		if record.Value.ExecutionHost != want {
			t.Fatalf("host = %q; want %q", record.Value.ExecutionHost, want)
		}
	}
	reserved := false
	boundary := &launcher.KubernetesBoundary{Kube: kube, Karpenter: provisioner, Namespace: cfg.Namespace,
		Probe: func(_ context.Context, r launcher.CloudNodeReservation) (launcher.CloudNetworkEvidence, error) {
			reserved = true
			host("")
			return launcher.CloudNetworkEvidence{Namespace: cfg.Namespace, NodeUID: r.NodeUID, PolicyRevision: "verified", DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
		}, Clear: func(context.Context, launcher.CloudNodeReservation) error { return nil },
	}
	runtime, err := launcher.NewLauncher(t.Context(), launcher.RuntimeConfig{Kube: kube, Boundary: boundary, Jobs: cfg, Demand: controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30}, Bearer: func(context.Context) (string, error) { return root.Raw, nil }, NewController: func(options client.EngineOptions) (launcher.LauncherController, error) {
		return cloudStoreClientW8(t, server)(options)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := runtime.LaunchOne(t.Context()); !claimed || !errors.Is(err, launcher.ErrNoDedicatedNode) {
		t.Fatalf("capacity wait: claimed=%t: %v", claimed, err)
	}
	host("")
	pods, err := kube.CoreV1().Pods(cfg.Namespace).List(t.Context(), metav1.ListOptions{LabelSelector: "sparkwing.dev/capacity-request=true"})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("capacity Pod: %+v %v", pods, err)
	}
	pod := pods.Items[0].DeepCopy()
	pod.UID = "capacity-pod"
	// A capacity Pod may even land on a different node than the eventual reservation.
	pod.Spec.NodeName = "provisional-capacity-node"
	if _, err := kube.CoreV1().Pods(cfg.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Sync(t.Context()); err != nil {
		t.Fatalf("capacity observation: %v", err)
	}
	host("")
	var demand struct {
		Binding launcher.CloudJobBinding `json:"binding"`
	}
	if err := json.Unmarshal([]byte(pod.Annotations["sparkwing.dev/capacity-demand"]), &demand); err != nil {
		t.Fatal(err)
	}
	service, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: cfg.ControllerURL, Launcher: true, Bearer: func(context.Context) (string, error) { return root.Raw, nil }})
	if err != nil {
		t.Fatal(err)
	}
	fence := demand.Binding.LeaseRef()
	if _, err := service.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{Capacity: []controllerwire.LauncherCapacityObservation{{Award: fence}}, Activations: []controllerwire.LauncherActivation{{Award: fence, NodeUID: "capacity-pod", ReservationID: "invented"}}}); err == nil {
		t.Fatal("capacity observation activated a provisional host")
	}
	host("")
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.LaunchOne(t.Context()); err != nil {
		t.Fatalf("reserved activation: %v", err)
	}
	if !reserved {
		t.Fatal("execution never reserved its node")
	}
	host("node:execution-node")
	secrets, err := kube.CoreV1().Secrets(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 {
		t.Fatalf("bootstrap: %+v %v", secrets, err)
	}
	bootstrap, err := launcher.ReadBootstrapAward(writeBootstrapW8(t, secrets.Items[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: cfg.ControllerURL, Bearer: func(context.Context) (string, error) { return string(bootstrap.Bootstrap), nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Exchange(t.Context()); err != nil {
		t.Fatal(err)
	}
	host("node:execution-node")
	if err := runtime.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	host("node:execution-node")
}
