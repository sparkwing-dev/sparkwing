package acceptance_test

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

type publicationKubeW8 struct {
	kubernetes.Interface
	beforeSecret func()
}

func (k publicationKubeW8) CoreV1() typedcore.CoreV1Interface {
	return publicationCoreW8{k.Interface.CoreV1(), k.beforeSecret}
}

type publicationCoreW8 struct {
	typedcore.CoreV1Interface
	beforeSecret func()
}

func (c publicationCoreW8) Secrets(namespace string) typedcore.SecretInterface {
	return publicationSecretsW8{c.CoreV1Interface.Secrets(namespace), c.beforeSecret}
}

type publicationSecretsW8 struct {
	typedcore.SecretInterface
	beforeCreate func()
}

func (s publicationSecretsW8) Create(ctx context.Context, secret *corev1.Secret, options metav1.CreateOptions) (*corev1.Secret, error) {
	s.beforeCreate()
	return s.SecretInterface.Create(ctx, secret, options)
}

func TestLauncherPublicationSurvivesConcurrentReconciliationW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			_, cfg, award := cloudFixture(kind)
			kube := fake.NewClientset()
			boundary := &cloudBoundaryFake{}
			control := &cloudControlW8{award: &award}
			reconciler := &launcher.Launcher{Kube: kube, Control: control, Boundary: boundary, Holder: "another-launcher", Config: cfg}
			observed := false
			publishing := &launcher.Launcher{Control: control, Boundary: boundary, Holder: award.Binding.Holder, Config: cfg}
			publishing.Kube = publicationKubeW8{Interface: kube, beforeSecret: func() {
				observed = true
				if err := reconciler.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				jobs, err := kube.BatchV1().Jobs(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
				if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Spec.Suspend == nil || !*jobs.Items[0].Spec.Suspend {
					t.Fatalf("live suspended Job lost during publication: jobs=%+v err=%v", jobs, err)
				}
				if boundary.released != 0 {
					t.Fatal("live publication lost its reserved node")
				}
			}}
			if claimed, err := publishing.LaunchOne(t.Context()); !claimed || err != nil {
				t.Fatalf("publication failed: claimed=%t err=%v", claimed, err)
			}
			jobs, err := kube.BatchV1().Jobs(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
			if !observed || err != nil || len(jobs.Items) != 1 || *jobs.Items[0].Spec.Suspend {
				t.Fatalf("publication did not activate: observed=%t jobs=%+v err=%v", observed, jobs, err)
			}
			control.remove = true
			if err := reconciler.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if boundary.released != 1 {
				t.Fatalf("retired publication release count=%d", boundary.released)
			}
		})
	}
}

func TestLauncherCancellationDuringPublicationW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			_, cfg, award := cloudFixture(kind)
			kube := fake.NewClientset()
			boundary := &cloudBoundaryFake{}
			control := &cloudControlW8{award: &award}
			l := &launcher.Launcher{Control: control, Boundary: boundary, Holder: award.Binding.Holder, Config: cfg}
			l.Kube = publicationKubeW8{Interface: kube, beforeSecret: func() { control.remove = true }}
			if claimed, err := l.LaunchOne(t.Context()); !claimed || err == nil {
				t.Fatalf("cancelled publication succeeded: claimed=%t err=%v", claimed, err)
			}
			jobs, jobErr := kube.BatchV1().Jobs(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
			secrets, secretErr := kube.CoreV1().Secrets(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
			if jobErr != nil || secretErr != nil || len(jobs.Items) != 0 || len(secrets.Items) != 0 || boundary.released != 1 {
				t.Fatalf("cancelled publication leaked: jobs=%+v jobErr=%v secrets=%+v secretErr=%v releases=%d", jobs, jobErr, secrets, secretErr, boundary.released)
			}
			for _, action := range kube.Actions() {
				if action.GetResource().Resource == "jobs" && action.GetVerb() == "patch" {
					var patch struct {
						Spec struct{ Suspend *bool }
					}
					if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
						t.Fatal(err)
					}
					if patch.Spec.Suspend != nil && !*patch.Spec.Suspend {
						t.Fatal("cancelled Job activated")
					}
				}
			}
		})
	}
}
