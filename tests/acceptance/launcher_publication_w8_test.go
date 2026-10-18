package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func renamedBootstrapFixtureW8(t *testing.T) (*launcher.Launcher, *fake.Clientset, *cloudBoundaryFake, *cloudControlW8) {
	t.Helper()
	_, cfg, award := cloudFixture(launcher.CloudWork)
	kube := cloudKubeW8()
	kube.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.Name, secret.UID, secret.OwnerReferences = secret.Name+"-renamed", "secret-uid", nil
		return false, nil, nil
	})
	// safety: the fake tracker needs the API's finalizer retention to exercise retry after restart.
	kube.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		gvr := batchv1.SchemeGroupVersion.WithResource("jobs")
		object, err := kube.Tracker().Get(gvr, cfg.Namespace, action.(ktesting.DeleteAction).GetName())
		if err != nil {
			return true, nil, err
		}
		job := object.(*batchv1.Job).DeepCopy()
		stamp := metav1.Now()
		job.DeletionTimestamp = &stamp
		return true, nil, kube.Tracker().Update(gvr, job, cfg.Namespace)
	})
	boundary, control := &cloudBoundaryFake{}, &cloudControlW8{award: &award}
	l := &launcher.Launcher{Kube: kube, Control: control, Boundary: boundary, Config: cfg}
	return l, kube, boundary, control
}

func TestLauncherRetriesRenamedBootstrapCleanupW8Components(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replace), func(t *testing.T) {
			l, kube, boundary, control := renamedBootstrapFixtureW8(t)
			failure, deletes := errors.New("bootstrap deletion unavailable"), 0
			kube.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				deletion := action.(ktesting.DeleteAction)
				if !strings.HasSuffix(deletion.GetName(), "-renamed") {
					return false, nil, nil
				}
				deletes++
				options := deletion.GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "secret-uid" {
					t.Fatal("cleanup lost the created Secret's UID")
				}
				if deletes == 1 {
					return true, nil, failure
				}
				object, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), l.Config.Namespace, deletion.GetName())
				if err != nil {
					return true, nil, err
				}
				if object.(*corev1.Secret).UID != *options.Preconditions.UID {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, deletion.GetName(), errors.New("UID changed"))
				}
				return false, nil, nil
			})
			if claimed, err := l.LaunchOne(t.Context()); !claimed || !errors.Is(err, failure) {
				t.Fatalf("cleanup failure lost: claimed=%t err=%v", claimed, err)
			}
			if boundary.released != 0 {
				t.Fatal("node released before bootstrap cleanup")
			}
			secrets, err := kube.CoreV1().Secrets(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(secrets.Items) != 1 {
				t.Fatalf("failed deletion lost its target: %+v %v", secrets, err)
			}
			if replace {
				secret := secrets.Items[0].DeepCopy()
				secret.UID = "replacement-uid"
				if err := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), secret, l.Config.Namespace); err != nil {
					t.Fatal(err)
				}
			}
			control.remove = true
			restarted := &launcher.Launcher{Kube: l.Kube, Control: l.Control, Config: l.Config, Boundary: l.Boundary, Poll: l.Poll, Logger: l.Logger}
			err = restarted.Sync(t.Context())
			if replace && !apierrors.IsConflict(err) {
				t.Fatalf("replacement was not fenced: %v", err)
			}
			if !replace && err != nil {
				t.Fatalf("cleanup retry: %v", err)
			}
			wantSecrets, wantReleases, wantFinalizers := 0, 1, 0
			if replace {
				wantSecrets, wantReleases, wantFinalizers = 1, 0, 1
			}
			secrets, err = kube.CoreV1().Secrets(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || deletes != 2 || len(secrets.Items) != wantSecrets || boundary.released != wantReleases {
				t.Fatalf("cleanup ownership lost: secrets=%+v err=%v deletes=%d releases=%d", secrets, err, deletes, boundary.released)
			}
			jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(jobs.Items) != 1 || len(jobs.Items[0].Finalizers) != wantFinalizers {
				t.Fatalf("cleanup finalization: %+v %v", jobs, err)
			}
		})
	}
}

func TestLauncherReportsBootstrapRecordFailureW8Components(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		for _, dropped := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup_fails=%t/dropped=%t", cleanupFails, dropped), func(t *testing.T) {
				l, kube, boundary, _ := renamedBootstrapFixtureW8(t)
				recordFailure, cleanupFailure := errors.New("Job patch unavailable"), errors.New("Secret deletion unavailable")
				kube.PrependReactor("patch", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					if dropped {
						object, err := kube.Tracker().Get(batchv1.SchemeGroupVersion.WithResource("jobs"), l.Config.Namespace, action.(ktesting.PatchAction).GetName())
						return true, object, err
					}
					return true, nil, recordFailure
				})
				kube.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
					options := action.(ktesting.DeleteAction).GetDeleteOptions()
					if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "secret-uid" {
						t.Fatal("immediate cleanup lost the Secret's UID")
					}
					if cleanupFails {
						return true, nil, cleanupFailure
					}
					return false, nil, nil
				})
				claimed, err := l.LaunchOne(t.Context())
				if !claimed || err == nil || !strings.Contains(err.Error(), "-renamed") || !strings.Contains(err.Error(), "secret-uid") {
					t.Fatalf("record failure lost its cleanup target: claimed=%t err=%v", claimed, err)
				}
				if !dropped && !errors.Is(err, recordFailure) {
					t.Fatalf("record write failure lost: %v", err)
				}
				if cleanupFails && !errors.Is(err, cleanupFailure) {
					t.Fatalf("immediate cleanup failure lost: %v", err)
				}
				jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
				if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Spec.Suspend == nil || !*jobs.Items[0].Spec.Suspend || boundary.released != 0 {
					t.Fatalf("failed record lost ownership: jobs=%+v err=%v releases=%d", jobs, err, boundary.released)
				}
				wantSecrets := 0
				if cleanupFails {
					wantSecrets = 1
				}
				secrets, err := kube.CoreV1().Secrets(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
				if err != nil || len(secrets.Items) != wantSecrets {
					t.Fatalf("immediate cleanup: secrets=%+v err=%v", secrets, err)
				}
			})
		}
	}
}

func TestLauncherRefusesChangedBootstrapW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		for _, test := range []struct {
			name   string
			mutate func(*corev1.Secret)
		}{
			{"renamed", func(s *corev1.Secret) { s.Name += "-renamed"; s.UID = "renamed-secret" }},
			{"bytes", func(s *corev1.Secret) { s.Data["bootstrap"] = []byte("different-credential") }},
			{"award", func(s *corev1.Secret) { s.Data["award.json"] = []byte("different-execution-inputs") }},
			{"extra_data", func(s *corev1.Secret) { s.Data["other"] = []byte("extra") }},
			{"string_data", func(s *corev1.Secret) { s.StringData = map[string]string{"bootstrap": "different-credential"} }},
			{"mutable", func(s *corev1.Secret) { s.Immutable = new(false) }},
			{"unset_immutable", func(s *corev1.Secret) { s.Immutable = nil }},
			{"type", func(s *corev1.Secret) { s.Type = "example.com/another-type" }},
			{"owner", func(s *corev1.Secret) { s.OwnerReferences[0].UID = "another-job" }},
			{"unowned", func(s *corev1.Secret) { s.OwnerReferences = nil }},
			{"deleting", func(s *corev1.Secret) { stamp := metav1.Now(); s.DeletionTimestamp = &stamp }},
		} {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				_, cfg, award := cloudFixture(kind)
				kube := cloudKubeW8()
				kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					action.(ktesting.CreateAction).GetObject().(*batchv1.Job).UID = "job-uid"
					return false, nil, nil
				})
				kube.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
					test.mutate(action.(ktesting.CreateAction).GetObject().(*corev1.Secret))
					return false, nil, nil
				})
				boundary := &cloudBoundaryFake{}
				l := &launcher.Launcher{Kube: kube, Control: &cloudControlW8{award: &award}, Boundary: boundary, Config: cfg}
				claimed, err := l.LaunchOne(t.Context())
				if !claimed || err == nil {
					t.Fatalf("changed bootstrap activated: claimed=%t err=%v", claimed, err)
				}
				if strings.Contains(err.Error(), string(award.Bootstrap)) || strings.Contains(err.Error(), "different-credential") {
					t.Fatal("bootstrap refusal exposed credential bytes")
				}
				actions := kube.Actions()
				jobs, jobErr := kube.BatchV1().Jobs(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
				secrets, secretErr := kube.CoreV1().Secrets(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
				if jobErr != nil || secretErr != nil || len(jobs.Items) != 0 || len(secrets.Items) != 0 || boundary.released != 1 {
					t.Fatalf("refused publication leaked: jobs=%+v jobErr=%v secrets=%+v secretErr=%v releases=%d", jobs, jobErr, secrets, secretErr, boundary.released)
				}
				for _, action := range actions {
					if action.GetResource().Resource == "jobs" && action.GetVerb() == "patch" {
						var patch struct{ Spec struct{ Suspend *bool } }
						if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
							t.Fatal(err)
						}
						if patch.Spec.Suspend != nil && !*patch.Spec.Suspend {
							t.Fatal("Job became runnable with a changed bootstrap")
						}
					}
					if action.GetResource().Resource == "secrets" && (action.GetVerb() == "get" || action.GetVerb() == "list") {
						t.Fatal("launcher required Secret read access")
					}
				}
			})
		}
	}
}

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
			kube := cloudKubeW8()
			boundary := &cloudBoundaryFake{}
			control := &cloudControlW8{award: &award}
			reconciler := &launcher.Launcher{Kube: kube, Control: control, Boundary: boundary, Config: cfg}
			observed := false
			publishing := &launcher.Launcher{Control: control, Boundary: boundary, Config: cfg}
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
			kube := cloudKubeW8()
			boundary := &cloudBoundaryFake{}
			control := &cloudControlW8{award: &award}
			l := &launcher.Launcher{Control: control, Boundary: boundary, Config: cfg}
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
