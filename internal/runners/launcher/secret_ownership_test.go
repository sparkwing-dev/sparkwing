package launcher

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

func FuzzLauncherNeverDeletesUnpublishedSecret(f *testing.F) {
	f.Add("unrelated", true)
	f.Add("existing", false)
	f.Fuzz(func(t *testing.T, suffix string, rename bool) {
		if len(suffix) > 40 {
			return
		}
		name := "foreign-" + strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' {
				return r
			}
			return 'x'
		}, suffix) + "-secret"
		l, kube, _, control := fixture()
		if !rename {
			name = jobName(control.award.Binding)
		}
		foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: l.Config.Namespace, UID: types.UID("unowned")}, Data: map[string][]byte{"private": []byte("unrelated")}}
		if _, err := kube.CoreV1().Secrets(l.Config.Namespace).Create(t.Context(), foreign, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if rename {
			kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name = name
				return false, nil, nil
			})
		}
		if _, err := l.LaunchOne(t.Context()); err == nil {
			t.Fatal("invalid publication succeeded")
		}
		got, err := kube.CoreV1().Secrets(l.Config.Namespace).Get(t.Context(), name, metav1.GetOptions{})
		if err != nil || got.UID != foreign.UID {
			t.Fatalf("unowned Secret was deleted: %v", err)
		}
		for _, action := range kube.Actions() {
			if action.GetVerb() == "delete" && action.GetResource().Resource == "secrets" {
				t.Fatal("unpublished Secret selected for deletion")
			}
		}
	})
}
