package cluster

import "context"

type Metrics struct {
	OnlineNodes int
	RunningJobs int
	QueuedJobs  int
	CPUUsage    int
}

type Provider interface {
	Snapshot(context.Context) (Metrics, error)
}
