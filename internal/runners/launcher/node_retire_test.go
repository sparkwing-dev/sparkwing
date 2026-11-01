package launcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type terminalControl struct {
	CloudControl
	terminal bool
	err      error
}

func (c terminalControl) RunTerminal(context.Context, string, runspec.RunID) (bool, error) {
	return c.terminal, c.err
}

func TestRestartRetiresOnlyEmptySpentNodesOfTerminalRuns(t *testing.T) {
	for _, scenario := range []string{"terminal", "running", "controller unavailable", "daemon pod", "work pod", "terminal pod", "warm pod", "foreign team", "foreign run", "unlabeled", "missing annotation", "foreign pool", "claim UID changed", "node replaced", "concurrent reservation", "cordon conflict"} {
		t.Run(scenario, func(t *testing.T) {
			boundary, kube := boundaryFixture()
			binding := boundaryBinding("team")
			held, err := boundary.Reserve(t.Context(), binding, jobName(binding), CloudPlacement{RequestedClass: "2"})
			if err != nil {
				t.Fatal(err)
			}
			if err = boundary.Release(t.Context(), held); err != nil {
				t.Fatal(err)
			}
			l, _, _, control := fixture()
			l.Kube, l.Boundary = kube, boundary
			l.Control = terminalControl{CloudControl: control, terminal: scenario != "running"}
			if scenario == "controller unavailable" {
				l.Control = terminalControl{CloudControl: control, err: errors.New("unavailable")}
			}
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "foreign team":
				node.Labels[TeamNodeLabel] = TeamNodeValue("cloud", "foreign")
			case "foreign run":
				node.Labels[usedRunLabel] = runNodeValue("cloud", "foreign")
			case "unlabeled":
				delete(node.Labels, usedRunLabel)
			case "missing annotation":
				delete(node.Annotations, usedRunAnnotation)
			case "foreign pool":
				node.Labels[PoolNamespaceLabel] = "foreign"
			case "claim UID changed":
				node.OwnerReferences[0].UID = "replacement"
			}
			if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if scenario == "node replaced" || scenario == "concurrent reservation" {
				kube.PrependReactor("get", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					current, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("nodes"), "", "node")
					if err != nil {
						return true, nil, err
					}
					changed := current.(*corev1.Node).DeepCopy()
					if scenario == "node replaced" {
						changed.UID = "replacement"
					} else {
						changed.ResourceVersion = "999"
						changed.Annotations[reservationAnnotation] = "new reservation"
					}
					return true, changed, nil
				})
			}
			if scenario == "cordon conflict" {
				kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					candidate := action.(ktesting.UpdateAction).GetObject().(*corev1.Node)
					if !candidate.Spec.Unschedulable {
						t.Fatal("expected cordon update")
					}
					current, lookupErr := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("nodes"), "", "node")
					if lookupErr != nil {
						t.Fatal(lookupErr)
					}
					reserved := current.(*corev1.Node).DeepCopy()
					reserved.ResourceVersion = "999"
					reserved.Annotations[reservationAnnotation] = "new reservation"
					if updateErr := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("nodes"), reserved, ""); updateErr != nil {
						t.Fatal(updateErr)
					}
					return false, nil, nil
				})
			}
			if scenario == "daemon pod" || scenario == "work pod" || scenario == "terminal pod" || scenario == "warm pod" {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "occupant", Namespace: "foreign"}, Spec: corev1.PodSpec{NodeName: "node"}}
				if scenario == "daemon pod" {
					pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "daemon", UID: "daemon-uid", Controller: ptr(true)}}
				}
				if scenario == "terminal pod" {
					pod.Status.Phase = corev1.PodSucceeded
				}
				if scenario == "warm pod" {
					pod = sharedWarmPod()
				}
				if _, err = kube.CoreV1().Pods(pod.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			karpenter := boundary.Karpenter.(*dynamicfake.FakeDynamicClient)
			karpenter.PrependReactor("delete", "nodeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "claim-uid" {
					t.Fatalf("unfenced deletion: %+v", options)
				}
				return false, nil, nil
			})
			err = l.Sync(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, err = boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
			retired := scenario == "terminal" || scenario == "daemon pod"
			if apierrors.IsNotFound(err) != retired {
				t.Fatalf("retired=%v want=%v: %v", apierrors.IsNotFound(err), retired, err)
			}
			current, lookupErr := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if scenario == "work pod" || scenario == "terminal pod" || scenario == "warm pod" {
				if !current.Spec.Unschedulable {
					t.Fatal("occupied spent node was not cordoned")
				}
			}
			if scenario == "cordon conflict" && (current.Spec.Unschedulable || current.Annotations[reservationAnnotation] != "new reservation") {
				t.Fatalf("retirement overwrote concurrent reservation: %+v", current)
			}
			if scenario == "cordon conflict" {
				return
			}
			if err = l.Sync(t.Context()); err != nil {
				t.Fatalf("repeated reconciliation: %v", err)
			}
		})
	}
}

func TestRunRetirementQueryThroughController(t *testing.T) {
	e, _ := cloudResourceStore(t)
	raw := cloudResourceRoot(t, e)
	handler := controller.New(e.Store, nil).WithEngineController(e.EngineController).Handler()
	transport := &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	wire, err := client.NewEngineClient(client.EngineOptions{URL: "https://controller.example", Launcher: true, Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: transport})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "success", "failed", "cancelled", "dry_run"} {
		var finished any
		if status != "running" {
			finished = int64(1)
		}
		cloudResourceSQL(t, e.Store, `UPDATE runs SET status=?,finished_at=? WHERE team='a' AND id='run'`, status, finished)
		terminal, err := control.RunTerminal(t.Context(), "a", "run")
		if err != nil || terminal != (status != "running") {
			t.Fatalf("%s: terminal=%v, %v", status, terminal, err)
		}
	}
	for _, ref := range []controllerwire.LauncherRun{{Team: "foreign", RunID: "run"}, {Team: "a", RunID: "missing"}} {
		terminal, err := control.RunTerminal(t.Context(), ref.Team, runspec.RunID(ref.RunID))
		if err != nil || terminal {
			t.Fatalf("foreign/missing %+v: %v, %v", ref, terminal, err)
		}
	}
	if _, err = wire.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{RetireRun: &controllerwire.LauncherRun{Team: "a", RunID: "run"}, WarmPools: map[string]int64{"sparkwing-2cpu": 0}}); err == nil {
		t.Fatal("mixed retirement query accepted")
	}
	cloudResourceSQL(t, e.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix='launcher'`)
	if terminal, err := control.RunTerminal(t.Context(), "a", "run"); err == nil || terminal {
		t.Fatalf("revoked launcher: %v, %v", terminal, err)
	}
}

func TestRunRetirementRefusalIsNotTerminal(t *testing.T) {
	wire := launcherWireFake{sync: func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
		return controllerwire.LauncherSyncResponse{}, &apierr.Error{Code: apierr.Forbidden, Message: "claimant repository scope excludes work"}
	}}
	control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "dev", Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil {
		t.Fatal(err)
	}
	if terminal, err := control.RunTerminal(t.Context(), "team", "foreign-run"); err != nil || terminal {
		t.Fatalf("scope refusal: terminal=%v error=%v", terminal, err)
	}
}

type countingTerminalControl struct {
	CloudControl
	calls    map[controllerwire.LauncherRun]int
	terminal bool
	err      error
}

func (c *countingTerminalControl) RunTerminal(_ context.Context, team string, run runspec.RunID) (bool, error) {
	c.calls[controllerwire.LauncherRun{Team: team, RunID: string(run)}]++
	return c.terminal, c.err
}

func TestSpentNodeQueriesEachRunOnceAndCachesNonTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boundary, kube := boundaryFixture()
		binding := boundaryBinding("team")
		held, err := boundary.Reserve(t.Context(), binding, jobName(binding), CloudPlacement{RequestedClass: "2"})
		if err != nil {
			t.Fatal(err)
		}
		if err = boundary.Release(t.Context(), held); err != nil {
			t.Fatal(err)
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		node.Name, node.UID, node.ResourceVersion = "second", "second-uid", ""
		if _, err = kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		l, _, _, control := fixture()
		counted := &countingTerminalControl{CloudControl: control, calls: make(map[controllerwire.LauncherRun]int)}
		l.Kube, l.Boundary, l.Control = kube, boundary, counted
		ref := controllerwire.LauncherRun{Team: "team", RunID: "run"}
		for i := range 3 {
			if err = l.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if counted.calls[ref] != 1 {
				t.Fatalf("pass %d queried same run %d times", i, counted.calls[ref])
			}
			time.Sleep(5 * time.Second)
		}
		counted.terminal = true
		time.Sleep(15 * time.Second)
		if err = l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		if counted.calls[ref] != 2 {
			t.Fatalf("expired non-terminal answer was not refreshed: %d", counted.calls[ref])
		}
		if _, err = boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("terminal run retained its empty node: %v", err)
		}
		counted.terminal, counted.err = false, errors.New("unavailable")
		for want := 3; want <= 4; want++ {
			if err = l.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if counted.calls[ref] != want {
				t.Fatalf("failed query count=%d want=%d", counted.calls[ref], want)
			}
		}
	})
}

func TestSpentNodeErrorsAreRateLimitedAndDoNotFailSync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boundary, kube := boundaryFixture()
		kube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.(ktesting.ListAction).GetListRestrictions().Labels.String() == "" {
				return false, nil, nil
			}
			return true, nil, errors.New("retirement unavailable")
		})
		l, _, _, control := fixture()
		var logs bytes.Buffer
		l.Kube, l.Boundary, l.Control = kube, boundary, terminalControl{CloudControl: control}
		l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		for range 3 {
			if err := l.Sync(t.Context()); err != nil {
				t.Fatalf("retirement blocked sync: %v", err)
			}
		}
		if got := bytes.Count(logs.Bytes(), []byte("retirement unavailable")); got != 1 {
			t.Fatalf("logged error %d times", got)
		}
		time.Sleep(time.Minute)
		if err := l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := bytes.Count(logs.Bytes(), []byte("retirement unavailable")); got != 2 {
			t.Fatalf("persistent error was not logged again: %d", got)
		}
	})
}

type blockedTerminalControl struct {
	*controlFake
	mu      sync.Mutex
	queried []time.Duration
	claimed []time.Time
}

func (c *blockedTerminalControl) RunTerminal(ctx context.Context, _ string, _ runspec.RunID) (bool, error) {
	start := time.Now()
	<-ctx.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queried = append(c.queried, time.Since(start))
	return false, ctx.Err()
}

func (c *blockedTerminalControl) Claim(context.Context, string, []string) (*CloudJobAward, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claimed = append(c.claimed, time.Now())
	return nil, nil
}

func TestSlowRunQueryDoesNotDelayReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
		boundary, nodes := boundaryFixture()
		node, err := nodes.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err = kube.Tracker().Add(node); err != nil {
			t.Fatal(err)
		}
		boundary.Kube, l.Boundary = kube, boundary
		binding := boundaryBinding("team")
		held, err := boundary.Reserve(t.Context(), binding, jobName(binding), CloudPlacement{RequestedClass: "2"})
		if err != nil {
			t.Fatal(err)
		}
		if err = boundary.Release(t.Context(), held); err != nil {
			t.Fatal(err)
		}
		blocked := &blockedTerminalControl{controlFake: control}
		l.Control, l.Poll = blocked, time.Second
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		start := time.Now()
		go func() { done <- l.Run(ctx) }()
		time.Sleep(retirementCallTimeout + time.Second)
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		blocked.mu.Lock()
		defer blocked.mu.Unlock()
		if len(blocked.queried) != 1 || blocked.queried[0] != retirementCallTimeout {
			t.Fatalf("run queries %v, want one bounded by %v", blocked.queried, retirementCallTimeout)
		}
		if len(blocked.claimed) < 10 || blocked.claimed[9].Sub(start) >= retirementCallTimeout {
			t.Fatalf("claims %v waited on the blocked run query", blocked.claimed)
		}
	})
}
