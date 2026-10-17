package acceptance_test

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cloudKubeW8(objects ...runtime.Object) *fake.Clientset {
	objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}})
	kube := fake.NewClientset(objects...)
	jobs := 0
	kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		creation := action.(ktesting.CreateAction)
		switch object := creation.GetObject().(type) {
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
