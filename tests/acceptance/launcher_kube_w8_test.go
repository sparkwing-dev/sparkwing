package acceptance_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func cloudKubeW8(objects ...runtime.Object) *fake.Clientset {
	policy, binding := launcher.AdmissionExemptionPolicy("cloud"), launcher.AdmissionExemptionBinding("cloud")
	objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}}, policy, binding)
	kube := fake.NewClientset(objects...)
	jobs := 0
	kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		creation := action.(ktesting.CreateAction)
		switch object := creation.GetObject().(type) {
		case *corev1.LimitRange:
			if action.GetNamespace() == "cloud" && strings.HasPrefix(object.Name, "sparkwing-exemption-probe-") && slices.Equal(action.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions().DryRun, []string{metav1.DryRunAll}) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "limitranges"}, object.Name, errors.New("ValidatingAdmissionPolicy '"+policy.Name+"' with binding '"+binding.Name+"' denied request: Cloud LimitRanges are forbidden"))
			}
		case *batchv1.Job:
			jobs++
			if object.UID == "" {
				object.UID = "job-uid"
				if jobs > 1 {
					object.UID = types.UID(fmt.Sprintf("job-uid-%d", jobs))
				}
			}
		case *corev1.Secret:
			if object.UID == "" {
				object.UID = types.UID("secret-uid-" + object.Name)
			}
		}
		return false, nil, nil
	})
	return kube
}
