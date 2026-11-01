package launcher

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func (l *Launcher) reportWarmPools(ctx context.Context) error {
	reporter, ok := l.Control.(interface {
		ReportWarmPools(context.Context, map[string]int64) error
	})
	if !ok {
		return nil
	}
	nodes, err := l.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: PoolNamespaceLabel + "=" + l.Config.Namespace + "," + CloudClassLabel})
	if err != nil {
		return err
	}
	warm := make(map[string]int64)
	for _, size := range match.CloudSizes() {
		warm[size.Label] = 0
	}
	for _, node := range nodes.Items {
		class := fmt.Sprintf("sparkwing-%scpu", node.Labels[CloudClassLabel])
		if _, known := warm[class]; !known {
			continue
		}
		if !nodeReady(node) || quarantined(node) || node.Labels[TeamNodeLabel] != "" || node.Labels[usedTeamLabel] != "" || node.Labels[usedRunLabel] != "" || node.Annotations[reservationAnnotation] != "" || slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.Key == reservationTaint }) {
			continue
		}
		warm[class]++
	}
	return reporter.ReportWarmPools(ctx, warm)
}

// ReportWarmPools sends a complete deployment sample through launcher authority.
func (c *ControllerControl) ReportWarmPools(ctx context.Context, warm map[string]int64) error {
	_, err := c.client.SyncLauncher(ctx, controllerwire.LauncherSyncRequest{WarmPools: warm})
	return err
}
