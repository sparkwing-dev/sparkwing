package launcher

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type teamFaults struct {
	counted, permitted map[string]bool
}

type teamFaultLedger struct {
	labels map[string]string
	teams  map[string]*teamFaults
}

func newTeamFaultLedger(nodes []corev1.Node) *teamFaultLedger {
	l := &teamFaultLedger{labels: make(map[string]string, len(nodes)), teams: map[string]*teamFaults{}}
	for _, node := range nodes {
		l.labels[node.Name] = node.Labels[TeamNodeLabel]
	}
	return l
}

// safety: only protected labels attribute faults; damaged annotations never select a team's budget.
func (l *teamFaultLedger) team(node string) *teamFaults {
	label := l.labels[node]
	if label == "" {
		return nil
	}
	if l.teams[label] == nil {
		l.teams[label] = &teamFaults{counted: map[string]bool{}, permitted: map[string]bool{}}
	}
	return l.teams[label]
}

func (l *teamFaultLedger) record(err error) {
	switch fault := err.(type) { //nolint:errorlint // a wrapped node fault remains a blocking error
	case *nodeFault:
		if team := l.team(fault.node); team != nil {
			team.counted[fault.node] = true
		}
	case interface{ Unwrap() []error }:
		for _, inner := range fault.Unwrap() {
			l.record(inner)
		}
	}
}

func (l *teamFaultLedger) quarantine(ctx context.Context, kube kubernetes.Interface, node *corev1.Node, cause string) error {
	fault := &nodeFault{node: node.Name, cause: cause}
	l.record(fault)
	team := l.team(node.Name)
	if team == nil {
		return fault
	}
	// safety: counting a teardown fault never grants permission to mutate another node.
	if !team.permitted[node.Name] && len(team.permitted) >= maxQuarantinesPerSync {
		fault.cause += fmt.Sprintf("; more than %d nodes faulted for this team, so its claims stop until an operator checks these nodes", maxQuarantinesPerSync)
		return fault
	}
	team.permitted[node.Name] = true
	err := quarantineNode(ctx, kube, node, cause)
	l.record(err)
	return err
}

func (l *teamFaultLedger) blockedLabels() []string {
	var labels []string
	for label, team := range l.teams {
		if len(team.counted) > maxQuarantinesPerSync {
			labels = append(labels, label)
		}
	}
	slices.Sort(labels)
	return labels
}
