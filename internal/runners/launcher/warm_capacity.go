package launcher

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const (
	warmClassLabel     = "sparkwing.dev/hardware-class"
	warmNamespaceLabel = "sparkwing.dev/cloud-namespace"
)

func (l *Launcher) reportWarmPools(ctx context.Context) error {
	reporter, ok := l.Control.(interface {
		ReportWarmPools(context.Context, map[string]int64) error
	})
	if !ok {
		return nil
	}
	nodes, err := l.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: warmNamespaceLabel + "=" + l.Config.Namespace + "," + warmClassLabel})
	if err != nil {
		return err
	}
	warm := make(map[string]int64)
	for _, size := range match.CloudSizes() {
		warm[size.Label] = 0
	}
	for _, node := range nodes.Items {
		class := node.Labels[warmClassLabel]
		if _, known := warm[class]; !known {
			continue
		}
		if !nodeReady(node) || quarantined(node) || node.Labels[TeamNodeLabel] != "" || node.Labels[usedTeamLabel] != "" || node.Labels[usedRunLabel] != "" || node.Annotations[reservationAnnotation] != "" {
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
