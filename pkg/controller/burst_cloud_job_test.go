//go:build unix

package controller_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func configureBurstCloudJob(t *testing.T, proof *controller.BurstExecutionProof) {
	t.Helper()
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}}, launcher.AdmissionExemptionPolicy("cloud"), launcher.AdmissionExemptionBinding("cloud"))
	kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, k8sruntime.Object, error) {
		switch object := action.(ktesting.CreateAction).GetObject().(type) {
		case *batchv1.Job:
			object.UID = "burst-job-uid"
		case *corev1.Secret:
			object.UID = "burst-bootstrap-uid"
		}
		return false, nil, nil
	})
	kube.PrependReactor("create", "limitranges", func(action ktesting.Action) (bool, k8sruntime.Object, error) {
		if len(action.(ktesting.CreateActionImpl).CreateOptions.DryRun) == 0 {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(corev1.Resource("limitranges"), "probe", errors.New("ValidatingAdmissionPolicy 'sparkwing-cloud-exemption-cloud' with binding 'sparkwing-cloud-exemption-cloud' denied request:"))
	})
	boundary := &burstCloudBoundary{}
	var runtime *launcher.Launcher
	var published launcher.CloudJobAward
	proof.ClaimCloud = func(options client.EngineOptions) controllerwire.ClaimedUnit {
		wire, err := client.NewEngineClient(options)
		if err != nil {
			t.Fatal(err)
		}
		observed := &burstLauncherWire{Controller: wire}
		image := "runner@sha256:" + strings.Repeat("a", 64)
		runtime, err = launcher.NewLauncher(t.Context(), launcher.RuntimeConfig{Kube: kube, Boundary: boundary, Jobs: launcher.CloudJobConfig{Namespace: "cloud", RunnerImage: image, DockerImage: image, ControllerURL: options.URL, CPUCeiling: 64, MemoryCeiling: 64 << 30}, Demand: controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 64}, HTTP: options.HTTP, Bearer: options.Bearer, NewController: func(client.EngineOptions) (launcher.LauncherController, error) { return observed, nil }})
		if err != nil {
			t.Fatal(err)
		}
		if launched, err := runtime.LaunchOne(t.Context()); err != nil || !launched {
			t.Fatalf("burst Cloud Job publication: launched=%v: %v", launched, err)
		}
		jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
		if err != nil || len(jobs.Items) != 1 {
			t.Fatalf("published Cloud Jobs: %+v %v", jobs, err)
		}
		job := jobs.Items[0]
		if job.Spec.Suspend == nil || *job.Spec.Suspend || job.Spec.Template.Spec.ServiceAccountName != launcher.ServiceAccount || len(job.Spec.Template.Spec.InitContainers) != 2 || job.Spec.Template.Spec.InitContainers[1].Args[0] != "fetch-source" || job.Spec.Template.Spec.Containers[0].Args[0] != "run-node" {
			t.Fatal("burst did not publish the source-init and execution Job")
		}
		secret, err := kube.CoreV1().Secrets("cloud").Get(t.Context(), job.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		root, err := options.Bearer(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range secret.Data {
			if strings.Contains(string(body), "requesting-machine-only-secret") || strings.Contains(string(body), root) {
				t.Fatal("Cloud Job carried a requester secret or launcher root")
			}
		}
		directory := t.TempDir()
		for name, body := range secret.Data {
			if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		published, err = launcher.ReadBootstrapAward(filepath.Join(directory, "bootstrap"))
		if err != nil {
			t.Fatal(err)
		}
		if observed.awarded == nil || string(published.Bootstrap) != observed.awarded.Capability || published.Source.BundleDigest == "" {
			t.Fatal("published bootstrap lost its claimed unit or submitted source")
		}
		return *observed.awarded
	}
	proof.ExchangeCloud = func(options client.EngineOptions, bootstrap controllerwire.ClaimedUnit) controllerwire.ClaimedUnit {
		destination := filepath.Join(t.TempDir(), "cloud-source")
		init := launcher.SourceInit{Bind: func(ctx context.Context, award launcher.CloudJobAward) (source.Options, func() error, error) {
			access, err := client.NewSourceAccess(options, award.Binding.RunID, award.Source)
			return source.Options{Access: access, BundleTransport: options.HTTP.Transport, BuildEnvironment: sourcetest.BuildEnvironment}, nil, err
		}}
		if err := init.Run(t.Context(), published, destination); err != nil {
			t.Fatalf("burst Cloud source init: %v", err)
		}
		if sha := sourcetest.Git(t, destination, "rev-parse", "HEAD"); sha != published.Source.SHA {
			t.Fatalf("Cloud initialized %s, want submitted %s", sha, published.Source.SHA)
		}
		var unit controllerwire.ClaimedUnit
		host := launcher.BootstrapHost{ControllerURL: options.URL, HTTP: options.HTTP, Host: burstCloudHost(func(_ context.Context, award controllerwire.ClaimedUnit) error { unit = award; return nil })}
		if err := host.Run(t.Context(), published); err != nil {
			t.Fatalf("burst Cloud bootstrap host: %v", err)
		}
		if unit.Capability == "" || unit.Capability == bootstrap.Capability || !reflect.DeepEqual(unit.Binding, bootstrap.Binding) {
			t.Fatal("Cloud exchange changed the claim fence or retained bootstrap authority")
		}
		return unit
	}
	proof.FinishCloud = func() {
		if err := runtime.Sync(t.Context()); err != nil {
			t.Fatalf("retire completed burst Cloud Job: %v", err)
		}
		jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
		if err != nil || len(jobs.Items) != 0 {
			t.Fatalf("finished burst retained Cloud Job: %+v %v", jobs, err)
		}
		secrets, err := kube.CoreV1().Secrets("cloud").List(t.Context(), metav1.ListOptions{})
		if err != nil || len(secrets.Items) != 0 || boundary.releases != 1 {
			t.Fatalf("finished burst retained bootstrap or node: secrets=%+v release=%d error=%v", secrets, boundary.releases, err)
		}
	}
}

type burstLauncherWire struct {
	controllerwire.Controller
	awarded *controllerwire.ClaimedUnit
}

func (w *burstLauncherWire) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	response, err := w.Controller.Claim(ctx, request)
	w.awarded = response.Claim
	return response, err
}

type burstCloudBoundary struct{ releases int }

func (*burstCloudBoundary) Reserve(_ context.Context, binding launcher.CloudJobBinding, job string) (launcher.CloudNodeReservation, launcher.CloudNetworkEvidence, error) {
	return launcher.CloudNodeReservation{NodeName: "cloud-node", NodeUID: "cloud-node-uid", Team: binding.Team, JobName: job, ReservationID: "burst-reservation", Award: binding.LeaseRef()}, launcher.CloudNetworkEvidence{Namespace: "cloud", NodeUID: "cloud-node-uid", PolicyRevision: "fixture-probe", DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
}

func (b *burstCloudBoundary) Release(context.Context, launcher.CloudNodeReservation) error {
	b.releases++
	return nil
}

type burstCloudHost func(context.Context, controllerwire.ClaimedUnit) error

func (h burstCloudHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return h(ctx, unit)
}
